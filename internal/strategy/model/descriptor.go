package model

import "errors"

var ErrInvalidDescriptor = errors.New("invalid strategy descriptor")

type Descriptor struct {
	Manifest      VersionManifest
	VersionID     VersionID
	Subscriptions SubscriptionSpec
}

func NewDescriptor(
	manifest VersionManifest,
	subscriptions SubscriptionSpec,
) (Descriptor, error) {
	versionID, err := NewVersionID(manifest)
	if err != nil || subscriptions.IsZero() {
		return Descriptor{}, ErrInvalidDescriptor
	}
	return Descriptor{
		Manifest: manifest, VersionID: versionID, Subscriptions: subscriptions,
	}, nil
}

func (descriptor Descriptor) Validate() error {
	versionID, err := NewVersionID(descriptor.Manifest)
	if err != nil || descriptor.VersionID != versionID || descriptor.Subscriptions.IsZero() {
		return ErrInvalidDescriptor
	}
	return nil
}
