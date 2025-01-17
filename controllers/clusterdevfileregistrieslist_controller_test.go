//
//
// Copyright 2022-2023 Red Hat, Inc.
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

package controllers

import (
	"fmt"

	"github.com/devfile/registry-operator/api/v1alpha1"
	. "github.com/devfile/registry-operator/pkg/test"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("ClusterDevfileRegistriesList controller test", func() {
	Context("When Creating ClusterDevfileRegistriesList CR with valid values", func() {
		It("Should return a status saying URL is reachable", func() {
			Expect(k8sClient.Create(ctx, getClusterDevfileRegistriesListCR(clusterdevfileRegistriesListName, devfileRegistriesNamespace,
				devfileStagingRegistryName, devfileStagingRegistryURL))).Should(Succeed())
			drlLookupKey := types.NamespacedName{Name: clusterdevfileRegistriesListName, Namespace: devfileRegistriesNamespace}
			//validate success status
			validateStatus(drlLookupKey, ClusterListType, allRegistriesReachable)
			//delete all crs
			deleteCRList(drlLookupKey, ClusterListType)
		})
	})
	Context("When Devfile URL is unavailable", func() {
		It("Should return a status saying URL cannot be reached ", func() {
			//start mock index server
			testServer := GetNewUnstartedTestServer()
			Expect(testServer).ShouldNot(BeNil())
			testServer.Start()
			Expect(k8sClient.Create(ctx, getClusterDevfileRegistriesListCR(clusterdevfileRegistriesListName, devfileRegistriesNamespace,
				localRegistryName, testServer.URL))).Should(Succeed())

			drlLookupKey := types.NamespacedName{Name: clusterdevfileRegistriesListName, Namespace: devfileRegistriesNamespace}
			// validate success status
			validateStatus(drlLookupKey, ClusterListType, allRegistriesReachable)

			//shut down the mock server
			testServer.Close()

			//update the CR to trigger a reconcile
			drl := &v1alpha1.ClusterDevfileRegistriesList{}
			k8sClient.Get(ctx, drlLookupKey, drl)
			//update list in existing CR
			registriesList := drl.Spec.DevfileRegistries
			registriesList = append(registriesList, v1alpha1.DevfileRegistryService{Name: devfileStagingRegistryName, URL: devfileStagingRegistryURL})
			drl.Spec.DevfileRegistries = registriesList
			Expect(k8sClient.Update(ctx, drl)).Should(Succeed())
			// validate unreachable status
			validateStatus(drlLookupKey, ClusterListType, fmt.Sprintf(registryUnreachable, testServer.URL))

		})
	})
	Context("When all entries in DevfileRegistry service list is deleted", func() {
		It("Should return a status saying CR is empty", func() {
			drlLookupKey := types.NamespacedName{Name: clusterdevfileRegistriesListName, Namespace: devfileRegistriesNamespace}

			drl := &v1alpha1.ClusterDevfileRegistriesList{}
			k8sClient.Get(ctx, drlLookupKey, drl)
			registriesList := drl.Spec.DevfileRegistries
			//delete all entries in the registries list
			drl.Spec.DevfileRegistries = registriesList[:0]
			Expect(k8sClient.Update(ctx, drl)).Should(Succeed())
			//validate empty status
			validateStatus(drlLookupKey, ClusterListType, emptyStatus)
			//delete all crs
			deleteCRList(drlLookupKey, ClusterListType)
		})
	})

})
