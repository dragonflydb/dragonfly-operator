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
	// DragonflyPortName is the name of the port on which the Dragonfly instance listens
	DragonflyPortName = "redis"
	// DragonflyPort is the port on which Dragonfly listens
	DragonflyPort = 6379

	DragonflyAdminPortName = "admin"
	// DragonflyAdminPort is the admin port on which Dragonfly listens
	// IMPORTANT: This port should not be opened to non trusted networks.
	DragonflyAdminPort = 9999

	MemcachedPortName = "memcached"
	MemcachedPortArg  = "--memcached_port"

	DragonflyContainerName = "dragonfly"

	AclVolumeName = "dragonfly-acl"
	AclDir        = "/var/lib/dragonfly"
	AclFileName   = "dragonfly.acl"
	AclFileArg    = "--aclfile"

	SnapshotsVolumeName = "df"
	SnapshotsDir        = "/dragonfly/snapshots"
	SnapshotsDirArg     = "--dir"
	SnapshotsCronArg    = "--snapshot_cron"

	TLSVolumeName       = "dragonfly-tls"
	TLSDir              = "/etc/dragonfly-tls"
	TLSCACertPathArg    = "--tls_ca_cert_file"
	TLSCACertDir        = "/etc/dragonfly/tls"
	TLSCACertFileName   = "ca.crt"
	TLSCACertVolumeName = "client-ca-cert"
	TLSCertPathArg      = "--tls_cert_file"
	TLSCertFileName     = "tls.crt"
	TLSKeyPathArg       = "--tls_key_file"
	TLSKeyFileName      = "tls.key"
	TLSArg              = "--tls"
	NoTLSOnAdminPortArg = "--no_tls_on_admin_port"

	// DragonflyOperatorName is the name of the operator
	DragonflyOperatorName = "dragonfly-operator"

	// DragonflyImage is the default image of the Dragonfly to use
	DragonflyImage = "docker.dragonflydb.io/dragonflydb/dragonfly"

	// Recommended Kubernetes Application Labels
	// KubernetesAppNameLabel is the name of the application
	KubernetesAppNameLabelKey = "app.kubernetes.io/name"
	KubernetesAppName         = "dragonfly"

	// KubernetesAppVersionLabel is the version of the application
	KubernetesAppVersionLabelKey = "app.kubernetes.io/version"

	// KubernetesAppComponentLabel is the component of the application
	KubernetesAppComponentLabelKey = "app.kubernetes.io/component"
	KubernetesAppComponent         = "dragonfly"

	KubernetesAppInstanceLabelKey = "app.kubernetes.io/instance"

	// KubernetesManagedByLabel is the tool being used to manage the operation of an application
	KubernetesManagedByLabelKey = "app.kubernetes.io/managed-by"

	// KubernetesPartOfLabel is the name of a higher level application this one is part of
	KubernetesPartOfLabelKey = "app.kubernetes.io/part-of"
	KubernetesPartOf         = "dragonfly"

	MasterIpLabelKey      = "master-ip"
	DragonflyNameLabelKey = "app"

	MasterIpAnnotationKey = "operator.dragonflydb.io/masterIP"

	RoleLabelKey = "role"

	Master = "master"

	Replica = "replica"

	// TrafficLabelKey is used to gate traffic to pods via Service selector
	TrafficLabelKey = "traffic"
	TrafficEnabled  = "enabled"
	TrafficDisabled = "disabled"
)

var DefaultDragonflyArgs = []string{
	"--alsologtostderr",
	"--break_replication_on_master_restart=true",
	"--primary_port_http_enabled=false",
	fmt.Sprintf("--admin_port=%d", DragonflyAdminPort),
	"--admin_nopass",
}
