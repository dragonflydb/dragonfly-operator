/*
Copyright 2023 DragonflyDB authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

import "fmt"

const (
	// DragonflyPort is the port on which Dragonfly listens
	DragonflyPort = 6379

	// DragonflyAdminPort is the admin port on which Dragonfly listens
	// IMPORTANT: This port should not be opened to non trusted networks.
	DragonflyAdminPort = 9999

	// DragonflyPortName is the name of the port on which the Dragonfly instance listens
	DragonflyPortName = "redis"

	// DragonflyOperatorName is the name of the operator
	DragonflyOperatorName = "dragonfly-operator"

	// DragonflyImage is the default image of the Dragonfly to use
	DragonflyImage = "docker.dragonflydb.io/dragonflydb/dragonfly"

	// DragonflyHealthCheckPath is the path on which the Dragonfly exposes its health check
	DragonflyHealthCheckPath = "/health"

	// Recommended Kubernetes Application Labels
	// KubernetesAppNameLabel is the name of the application
	KubernetesAppNameLabelKey = "app.kubernetes.io/name"

	// KubernetesAppVersionLabel is the version of the application
	KubernetesAppVersionLabelKey = "app.kubernetes.io/version"

	// KubernetesAppComponentLabel is the component of the application
	KubernetesAppComponentLabelKey = "app.kubernetes.io/component"

	KubernetesAppInstanceNameLabel = "app.kubernetes.io/instance"

	// KubernetesManagedByLabel is the tool being used to manage the operation of an application
	KubernetesManagedByLabelKey = "app.kubernetes.io/managed-by"

	// KubernetesPartOfLabel is the name of a higher level application this one is part of
	KubernetesPartOfLabelKey = "app.kubernetes.io/part-of"

	DragonflyNameLabelKey = "app"

	MasterIp string = "master-ip"

	Role string = "role"

	Master string = "master"

	Replica string = "replica"

	DragonflyContainerName = "dragonfly"
)

var DefaultDragonflyArgs = []string{
	"--alsologtostderr",
	"--primary_port_http_enabled=false",
	fmt.Sprintf("--admin_port=%d", DragonflyAdminPort),
	"--admin_nopass",
}
