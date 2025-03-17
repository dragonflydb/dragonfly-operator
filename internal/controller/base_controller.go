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

import (
	"context"
	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type (
	Reconciler struct {
		Client        client.Client // Explicitly named
		Scheme        *runtime.Scheme
		EventRecorder record.EventRecorder
	}
	// DragonflyInstance is an abstraction over the `Dragonfly` CRD
	// and provides methods to handle replication.
	DragonflyInstance struct {
		// Dragonfly is the relevant Dragonfly CRD that it performs actions over
		df *dfv1alpha1.Dragonfly

		client        client.Client
		scheme        *runtime.Scheme
		eventRecorder record.EventRecorder
		log           logr.Logger
	}
)

func (r *Reconciler) getDragonflyInstance(ctx context.Context, namespacedName types.NamespacedName, log logr.Logger) (*DragonflyInstance, error) {
	// Retrieve the relevant Dragonfly object
	var df dfv1alpha1.Dragonfly
	err := r.Client.Get(ctx, namespacedName, &df)
	if err != nil {
		return nil, err
	}

	return &DragonflyInstance{
		df:            &df,
		client:        r.Client,
		scheme:        r.Scheme,
		eventRecorder: r.EventRecorder,
		log:           log,
	}, nil
}
