package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TestReconcilePasswordSecretCreatesGeneratedSecret(t *testing.T) {
	ctx := context.Background()
	dfi, c := newPasswordSecretTestInstance(t, &dfv1alpha1.Dragonfly{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dragonflydb.io/v1alpha1",
			Kind:       "Dragonfly",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "df-auto-secret",
			Namespace: "default",
			UID:       types.UID("df-auto-secret-uid"),
		},
		Spec: dfv1alpha1.DragonflySpec{
			Replicas: 1,
			Authentication: &dfv1alpha1.Authentication{
				AutoCreatePasswordSecret: true,
			},
		},
	})

	passwordHash, err := dfi.reconcilePasswordSecret(ctx)
	if err != nil {
		t.Fatalf("reconcilePasswordSecret() error = %v", err)
	}
	if passwordHash == "" {
		t.Fatal("reconcilePasswordSecret() returned an empty password hash")
	}

	var secret corev1.Secret
	if err := c.Get(ctx, ctrlclient.ObjectKey{Namespace: "default", Name: "df-auto-secret-password"}, &secret); err != nil {
		t.Fatalf("generated secret not found: %v", err)
	}

	if secret.Type != resources.GeneratedPasswordSecretType {
		t.Fatalf("unexpected secret type %q", secret.Type)
	}
	if secret.Data[resources.GeneratedPasswordUsernameKey] == nil {
		t.Fatalf("generated secret is missing %q", resources.GeneratedPasswordUsernameKey)
	}
	if string(secret.Data[resources.GeneratedPasswordUsernameKey]) != resources.GeneratedPasswordUsername {
		t.Fatalf("unexpected username %q", string(secret.Data[resources.GeneratedPasswordUsernameKey]))
	}
	if string(secret.Data[resources.GeneratedPasswordUserKey]) != resources.GeneratedPasswordUsername {
		t.Fatalf("unexpected user %q", string(secret.Data[resources.GeneratedPasswordUserKey]))
	}
	if len(secret.Data[resources.GeneratedPasswordSecretKey]) == 0 {
		t.Fatalf("generated secret is missing %q", resources.GeneratedPasswordSecretKey)
	}
	if string(secret.Data[resources.GeneratedPasswordHostKey]) != "df-auto-secret" {
		t.Fatalf("unexpected host %q", string(secret.Data[resources.GeneratedPasswordHostKey]))
	}
	if string(secret.Data[resources.GeneratedPasswordPortKey]) != "6379" {
		t.Fatalf("unexpected port %q", string(secret.Data[resources.GeneratedPasswordPortKey]))
	}
	if len(secret.Data[resources.GeneratedPasswordURIKey]) == 0 {
		t.Fatalf("generated secret is missing %q", resources.GeneratedPasswordURIKey)
	}
	if len(secret.Data[resources.GeneratedPasswordFQDNURIKey]) == 0 {
		t.Fatalf("generated secret is missing %q", resources.GeneratedPasswordFQDNURIKey)
	}
	if !metav1.IsControlledBy(&secret, dfi.df) {
		t.Fatalf("generated secret should be owned by dragonfly")
	}
}

func TestReconcilePasswordSecretRetainsOwnedGeneratedSecretWhenDisabled(t *testing.T) {
	ctx := context.Background()
	df := &dfv1alpha1.Dragonfly{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dragonflydb.io/v1alpha1",
			Kind:       "Dragonfly",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "df-auto-secret",
			Namespace: "default",
			UID:       types.UID("df-auto-secret-uid"),
		},
		Spec: dfv1alpha1.DragonflySpec{
			Replicas: 1,
		},
	}

	ownedSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "df-auto-secret-password",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: df.APIVersion,
				Kind:       df.Kind,
				Name:       df.Name,
				UID:        df.UID,
				Controller: boolPtr(true),
			}},
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{resources.GeneratedPasswordSecretKey: []byte("secret")},
	}

	dfi, c := newPasswordSecretTestInstance(t, df, &ownedSecret)

	passwordHash, err := dfi.reconcilePasswordSecret(ctx)
	if err != nil {
		t.Fatalf("reconcilePasswordSecret() error = %v", err)
	}
	if passwordHash != "" {
		t.Fatalf("expected no active generated password hash, got %q", passwordHash)
	}

	var secret corev1.Secret
	if err := c.Get(ctx, ctrlclient.ObjectKey{Namespace: "default", Name: "df-auto-secret-password"}, &secret); err != nil {
		t.Fatalf("expected generated secret to be retained: %v", err)
	}
}

func TestReconcilePasswordSecretRejectsUnownedNameCollision(t *testing.T) {
	df := newPasswordSecretTestDragonfly()
	unownedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "df-auto-secret-password", Namespace: "default"},
		Data:       map[string][]byte{resources.GeneratedPasswordSecretKey: []byte("unowned")},
	}
	dfi, _ := newPasswordSecretTestInstance(t, df, unownedSecret)

	_, err := dfi.reconcilePasswordSecret(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is not controlled") {
		t.Fatalf("expected ownership collision error, got %v", err)
	}
}

func TestReconcileResourcesKeepsGeneratedCredentialsWhenExplicitSecretIsInvalid(t *testing.T) {
	ctx := context.Background()
	df := newPasswordSecretTestDragonfly()
	dfi, c := newPasswordSecretTestInstance(t, df)

	if err := dfi.reconcileResources(ctx); err != nil {
		t.Fatalf("initial reconcileResources() error = %v", err)
	}
	var statefulSet appsv1.StatefulSet
	key := ctrlclient.ObjectKey{Namespace: df.Namespace, Name: df.Name}
	if err := c.Get(ctx, key, &statefulSet); err != nil {
		t.Fatalf("statefulset not found: %v", err)
	}
	originalSpec := statefulSet.Spec.DeepCopy()

	df.Spec.Authentication.PasswordFromSecret = &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "missing-secret"},
		Key:                  "password",
	}
	if err := dfi.reconcileResources(ctx); err == nil {
		t.Fatal("expected invalid explicit secret transition to fail")
	}
	if err := c.Get(ctx, key, &statefulSet); err != nil {
		t.Fatalf("statefulset not found after failed transition: %v", err)
	}
	if !reflect.DeepEqual(originalSpec, &statefulSet.Spec) {
		t.Fatal("failed explicit secret transition changed the statefulset")
	}
	var generated corev1.Secret
	if err := c.Get(ctx, ctrlclient.ObjectKey{
		Namespace: df.Namespace, Name: resources.GetGeneratedPasswordSecretName(df),
	}, &generated); err != nil {
		t.Fatalf("failed explicit secret transition removed generated credentials: %v", err)
	}
}

func TestReconcileResourcesSwitchesToExplicitSecretAndRetainsGeneratedSecret(t *testing.T) {
	ctx := context.Background()
	df := newPasswordSecretTestDragonfly()
	dfi, c := newPasswordSecretTestInstance(t, df)

	if err := dfi.reconcileResources(ctx); err != nil {
		t.Fatalf("initial reconcileResources() error = %v", err)
	}
	explicit := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "explicit-password", Namespace: df.Namespace},
		Data:       map[string][]byte{"secret-key": []byte("external-password")},
	}
	if err := c.Create(ctx, explicit); err != nil {
		t.Fatalf("failed to create explicit password secret: %v", err)
	}
	df.Spec.Authentication.PasswordFromSecret = &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: explicit.Name},
		Key:                  "secret-key",
	}

	if err := dfi.reconcileResources(ctx); err != nil {
		t.Fatalf("reconcileResources() during explicit secret transition error = %v", err)
	}
	var statefulSet appsv1.StatefulSet
	if err := c.Get(ctx, ctrlclient.ObjectKey{Namespace: df.Namespace, Name: df.Name}, &statefulSet); err != nil {
		t.Fatalf("statefulset not found: %v", err)
	}
	if _, ok := statefulSet.Spec.Template.Annotations[resources.GeneratedPasswordHashAnnotationKey]; ok {
		t.Fatal("generated password hash annotation was not removed")
	}
	foundExplicitSelector := false
	for _, env := range statefulSet.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "DFLY_requirepass" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			foundExplicitSelector = env.ValueFrom.SecretKeyRef.Name == explicit.Name && env.ValueFrom.SecretKeyRef.Key == "secret-key"
		}
	}
	if !foundExplicitSelector {
		t.Fatal("statefulset does not reference the explicit password secret")
	}
	var generated corev1.Secret
	if err := c.Get(ctx, ctrlclient.ObjectKey{
		Namespace: df.Namespace, Name: resources.GetGeneratedPasswordSecretName(df),
	}, &generated); err != nil {
		t.Fatalf("generated secret was not retained: %v", err)
	}
}

func TestReconcileResourcesRollsPodsWhenGeneratedPasswordChanges(t *testing.T) {
	ctx := context.Background()
	df := newPasswordSecretTestDragonfly()
	dfi, c := newPasswordSecretTestInstance(t, df)

	if err := dfi.reconcileResources(ctx); err != nil {
		t.Fatalf("initial reconcileResources() error = %v", err)
	}

	var statefulSet appsv1.StatefulSet
	key := ctrlclient.ObjectKey{Namespace: df.Namespace, Name: df.Name}
	if err := c.Get(ctx, key, &statefulSet); err != nil {
		t.Fatalf("statefulset not found: %v", err)
	}
	oldHash := statefulSet.Spec.Template.Annotations[resources.GeneratedPasswordHashAnnotationKey]
	if oldHash == "" {
		t.Fatal("statefulset is missing generated password hash annotation")
	}

	var secret corev1.Secret
	secretKey := ctrlclient.ObjectKey{Namespace: df.Namespace, Name: resources.GetGeneratedPasswordSecretName(df)}
	if err := c.Get(ctx, secretKey, &secret); err != nil {
		t.Fatalf("generated secret not found: %v", err)
	}
	secret.Data[resources.GeneratedPasswordSecretKey] = []byte("rotated-password")
	if err := c.Update(ctx, &secret); err != nil {
		t.Fatalf("failed to rotate generated password: %v", err)
	}

	if err := dfi.reconcileResources(ctx); err != nil {
		t.Fatalf("reconcileResources() after rotation error = %v", err)
	}
	if err := c.Get(ctx, key, &statefulSet); err != nil {
		t.Fatalf("statefulset not found after rotation: %v", err)
	}
	newHash := statefulSet.Spec.Template.Annotations[resources.GeneratedPasswordHashAnnotationKey]
	if newHash == oldHash {
		t.Fatalf("generated password rotation did not change pod template hash %q", newHash)
	}
	if strings.Contains(newHash, "rotated-password") {
		t.Fatal("generated password hash annotation contains the plaintext password")
	}

	df.Spec.Authentication.AutoCreatePasswordSecret = false
	if err := dfi.reconcileResources(ctx); err != nil {
		t.Fatalf("reconcileResources() after disabling generation error = %v", err)
	}
	if err := c.Get(ctx, key, &statefulSet); err != nil {
		t.Fatalf("statefulset not found after disabling generation: %v", err)
	}
	if _, ok := statefulSet.Spec.Template.Annotations[resources.GeneratedPasswordHashAnnotationKey]; ok {
		t.Fatal("generated password hash annotation was not removed")
	}
	if err := c.Get(ctx, secretKey, &secret); err != nil {
		t.Fatalf("generated secret was not retained: %v", err)
	}
}

func TestReconcilePasswordSecretRecreatesDeletedSecretWithNewHash(t *testing.T) {
	ctx := context.Background()
	df := newPasswordSecretTestDragonfly()
	dfi, c := newPasswordSecretTestInstance(t, df)

	oldHash, err := dfi.reconcilePasswordSecret(ctx)
	if err != nil {
		t.Fatalf("initial reconcilePasswordSecret() error = %v", err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: resources.GetGeneratedPasswordSecretName(df), Namespace: df.Namespace,
	}}
	if err := c.Delete(ctx, secret); err != nil {
		t.Fatalf("failed to delete generated secret: %v", err)
	}
	if err := c.Get(ctx, ctrlclient.ObjectKeyFromObject(secret), secret); !apierrors.IsNotFound(err) {
		t.Fatalf("expected generated secret to be deleted, got %v", err)
	}

	newHash, err := dfi.reconcilePasswordSecret(ctx)
	if err != nil {
		t.Fatalf("reconcilePasswordSecret() after deletion error = %v", err)
	}
	if newHash == oldHash {
		t.Fatalf("recreated secret reused password hash %q", newHash)
	}
}

func TestReconcilePasswordSecretPreservesCustomData(t *testing.T) {
	ctx := context.Background()
	df := newPasswordSecretTestDragonfly()
	dfi, c := newPasswordSecretTestInstance(t, df)

	originalHash, err := dfi.reconcilePasswordSecret(ctx)
	if err != nil {
		t.Fatalf("initial reconcilePasswordSecret() error = %v", err)
	}
	var secret corev1.Secret
	key := ctrlclient.ObjectKey{Namespace: df.Namespace, Name: resources.GetGeneratedPasswordSecretName(df)}
	if err := c.Get(ctx, key, &secret); err != nil {
		t.Fatalf("generated secret not found: %v", err)
	}
	secret.Data["application-config"] = []byte("preserve-me")
	if err := c.Update(ctx, &secret); err != nil {
		t.Fatalf("failed to add custom secret data: %v", err)
	}

	reconciledHash, err := dfi.reconcilePasswordSecret(ctx)
	if err != nil {
		t.Fatalf("reconcilePasswordSecret() error = %v", err)
	}
	if reconciledHash != originalHash {
		t.Fatalf("custom data changed generated password hash from %q to %q", originalHash, reconciledHash)
	}
	if err := c.Get(ctx, key, &secret); err != nil {
		t.Fatalf("generated secret not found after reconcile: %v", err)
	}
	if string(secret.Data["application-config"]) != "preserve-me" {
		t.Fatal("reconcile removed custom secret data")
	}
}

func TestReconcileResourcesValidatesBeforeCreatingSecret(t *testing.T) {
	ctx := context.Background()
	df := newPasswordSecretTestDragonfly()
	df.Spec.OwnedObjectsMetadata = &dfv1alpha1.MetadataSpec{
		Labels: map[string]string{resources.DragonflyNameLabelKey: "override"},
	}
	dfi, c := newPasswordSecretTestInstance(t, df)

	if err := dfi.reconcileResources(ctx); err == nil {
		t.Fatal("expected invalid owned resource metadata to fail")
	}
	var secret corev1.Secret
	err := c.Get(ctx, ctrlclient.ObjectKey{
		Namespace: df.Namespace, Name: resources.GetGeneratedPasswordSecretName(df),
	}, &secret)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("invalid specification created a generated secret: %v", err)
	}
}

func newPasswordSecretTestDragonfly() *dfv1alpha1.Dragonfly {
	return &dfv1alpha1.Dragonfly{
		TypeMeta: metav1.TypeMeta{APIVersion: "dragonflydb.io/v1alpha1", Kind: "Dragonfly"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "df-auto-secret", Namespace: "default", UID: types.UID("df-auto-secret-uid"),
		},
		Spec: dfv1alpha1.DragonflySpec{
			Replicas: 1,
			Authentication: &dfv1alpha1.Authentication{
				AutoCreatePasswordSecret: true,
			},
		},
		Status: dfv1alpha1.DragonflyStatus{Phase: PhaseResourcesCreated},
	}
}

func newPasswordSecretTestInstance(t *testing.T, df *dfv1alpha1.Dragonfly, initObjs ...ctrlclient.Object) (*DragonflyInstance, ctrlclient.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core scheme: %v", err)
	}
	if err := dfv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add dragonfly scheme: %v", err)
	}

	objs := append([]ctrlclient.Object{df}, initObjs...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	return &DragonflyInstance{
		df:            df,
		client:        c,
		log:           log.Log,
		scheme:        scheme,
		eventRecorder: record.NewFakeRecorder(8),
		clusterDomain: resources.DefaultKubernetesClusterDomain,
	}, c
}

func boolPtr(v bool) *bool {
	return &v
}
