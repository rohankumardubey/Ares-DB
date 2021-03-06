//  Copyright (c) 2017-2018 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"

	memCom "github.com/uber/aresdb/memstore/common"
	memMocks "github.com/uber/aresdb/memstore/mocks"
	metaCom "github.com/uber/aresdb/metastore/common"

	"github.com/uber/aresdb/metastore/mocks"

	"github.com/gorilla/mux"
	"github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = ginkgo.Describe("EnumHandler", func() {

	var testServer *httptest.Server
	var hostPort string
	var testTable = metaCom.Table{
		Name: "testTable",
		Columns: []metaCom.Column{
			{
				Name: "col1",
				Type: "Int32",
			},
		},
	}
	var testTableSchema = memCom.TableSchema{
		EnumDicts: map[string]memCom.EnumDict{
			"testColumn": {
				ReverseDict: []string{"a", "b", "c"},
				Dict: map[string]int{
					"a": 0,
					"b": 1,
					"c": 2,
				},
			},
		},
		Schema: testTable,
	}

	testMetastore := &mocks.MetaStore{}
	var testMemStore *memMocks.MemStore

	ginkgo.BeforeEach(func() {
		testMemStore = CreateMemStore(&testTableSchema, 0, nil, nil)
		enumHandler := NewEnumHandler(testMemStore, testMetastore)
		testRouter := mux.NewRouter()
		enumHandler.Register(testRouter.PathPrefix("/schema").Subrouter())
		testServer = httptest.NewUnstartedServer(WithPanicHandling(testRouter))
		testServer.Start()
		hostPort = testServer.Listener.Addr().String()
	})

	ginkgo.AfterEach(func() {
		testServer.Close()
	})

	ginkgo.It("ListEnumCases should work", func() {
		resp, _ := http.Get(fmt.Sprintf("http://%s/schema/tables/%s/columns/%s/enum-cases", hostPort, "testTable", "testColumn"))
		??(resp.StatusCode).Should(Equal(http.StatusOK))
		respBody, err := ioutil.ReadAll(resp.Body)
		??(err).Should(BeNil())
		var enumCases []string
		err = json.Unmarshal(respBody, &enumCases)
		??(err).Should(BeNil())
		??(enumCases).Should(Equal([]string{"a", "b", "c"}))

		resp, _ = http.Get(fmt.Sprintf("http://%s/schema/tables/%s/columns/%s/enum-cases", hostPort, "unknown", "testColumn"))
		??(resp.StatusCode).Should(Equal(http.StatusBadRequest))

		resp, _ = http.Get(fmt.Sprintf("http://%s/schema/tables/%s/columns/%s/enum-cases", hostPort, "testTable", "unknown"))
		??(resp.StatusCode).Should(Equal(http.StatusBadRequest))
	})

	ginkgo.It("AddEnumCase should work", func() {
		enumCases := []byte(`{"enumCases": ["a"]}`)
		errousEnumCases := []byte(`{"enumCases": ["a"`)
		testEnumID := []int{1}

		testMetastore.On("ExtendEnumDict", mock.Anything, mock.Anything, mock.Anything).Return(testEnumID, nil).Once()
		resp, _ := http.Post(fmt.Sprintf("http://%s/schema/tables/%s/columns/%s/enum-cases", hostPort, "testTable", "testColumn"), "application/json", bytes.NewBuffer(enumCases))
		??(resp.StatusCode).Should(Equal(http.StatusOK))

		resp, _ = http.Post(fmt.Sprintf("http://%s/schema/tables/%s/columns/%s/enum-cases", hostPort, "testTable", "testColumn"), "application/json", bytes.NewBuffer(errousEnumCases))
		??(resp.StatusCode).Should(Equal(http.StatusBadRequest))

		testMetastore.On("ExtendEnumDict", mock.Anything, mock.Anything, mock.Anything).Return(0, errors.New("Failed to extend enums")).Once()
		resp, _ = http.Post(fmt.Sprintf("http://%s/schema/tables/%s/columns/%s/enum-cases", hostPort, "testTable", "testColumn"), "application/json", bytes.NewBuffer(enumCases))
		??(resp.StatusCode).Should(Equal(http.StatusInternalServerError))
	})
})
