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

package controller

import "time"

const (
	// EndpointPropagationTimeout is the max time to wait for endpoint updates
	EndpointPropagationTimeout = 30 * time.Second
	// ConnectionDrainPeriod is the time to wait after disconnecting clients
	ConnectionDrainPeriod = 1 * time.Second
	// EndpointPollInterval is the interval between endpoint checks
	EndpointPollInterval = 500 * time.Millisecond
)
