// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package azure

import (
	"github.com/juju/collections/set"
	"github.com/juju/errors"

	"github.com/juju/juju/core/instance"
	"github.com/juju/juju/core/network"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/context"
	"github.com/juju/juju/internal/provider/azure/internal/errorutils"
	"github.com/juju/juju/internal/provider/common"
	"github.com/juju/juju/storage"
)

var (
	_ common.ZonedEnviron = (*azureEnviron)(nil)
	_ context.Distributor = (*azureEnviron)(nil)
)

// azureAvailabilityZone implements network.AvailabilityZone.
type azureAvailabilityZone struct {
	name string
}

// Name is part of the network.AvailabilityZone interface.
func (z azureAvailabilityZone) Name() string {
	return z.name
}

// Available is part of the network.AvailabilityZone interface.
func (z azureAvailabilityZone) Available() bool {
	return true
}

// useAvailabilityZones reports whether the model is configured to place
// machines into availability zones.
func (env *azureEnviron) useAvailabilityZones() bool {
	env.mu.Lock()
	defer env.mu.Unlock()
	return env.config.useAvailabilityZones
}

// AvailabilityZones is part of the common.ZonedEnviron interface.
//
// If the model is not configured to use availability zones, or the
// location has no availability zones, a NotImplemented error is
// returned. Callers treat this as the provider not supporting zones,
// and machines are placed into availability sets instead.
func (env *azureEnviron) AvailabilityZones(ctx context.ProviderCallContext) (network.AvailabilityZones, error) {
	env.mu.Lock()
	defer env.mu.Unlock()

	if !env.config.useAvailabilityZones {
		return nil, errors.NotImplementedf(
			"availability zones without %s=true", configAttrUseAvailabilityZones,
		)
	}
	if _, err := env.getInstanceTypesLocked(ctx); err != nil {
		return nil, errors.Annotate(err, "getting instance types")
	}

	allZones := set.NewStrings()
	for _, zones := range env.instanceTypeZones {
		allZones = allZones.Union(zones)
	}
	if allZones.IsEmpty() {
		logger.Warningf(
			"%s=true but location %q has no availability zones, using availability sets",
			configAttrUseAvailabilityZones, env.location,
		)
		return nil, errors.NotImplementedf(
			"availability zones in location %q", env.location,
		)
	}

	result := make(network.AvailabilityZones, 0, allZones.Size())
	for _, name := range allZones.SortedValues() {
		result = append(result, azureAvailabilityZone{name: name})
	}
	return result, nil
}

// InstanceAvailabilityZoneNames is part of the common.ZonedEnviron
// interface. Instances that are not in an availability zone are reported
// with an empty zone name.
func (env *azureEnviron) InstanceAvailabilityZoneNames(
	ctx context.ProviderCallContext, ids []instance.Id,
) (map[instance.Id]string, error) {
	if !env.useAvailabilityZones() {
		return nil, errors.NotImplementedf(
			"availability zones without %s=true", configAttrUseAvailabilityZones,
		)
	}
	if len(ids) == 0 {
		return nil, environs.ErrNoInstances
	}

	compute, err := env.computeClient()
	if err != nil {
		return nil, errors.Trace(err)
	}
	vmZones := make(map[instance.Id]string)
	pager := compute.NewListPager(env.resourceGroup, nil)
	for pager.More() {
		next, err := pager.NextPage(ctx)
		if err != nil {
			if errorutils.IsNotFoundError(err) {
				return nil, environs.ErrNoInstances
			}
			return nil, errorutils.HandleCredentialError(
				errors.Annotate(err, "listing virtual machines"), ctx,
			)
		}
		for _, vm := range next.Value {
			var zone string
			if len(vm.Zones) > 0 {
				zone = toValue(vm.Zones[0])
			}
			vmZones[instance.Id(toValue(vm.Name))] = zone
		}
	}

	result := make(map[instance.Id]string)
	for _, id := range ids {
		if zone, ok := vmZones[id]; ok {
			result[id] = zone
		}
	}
	switch {
	case len(result) == 0:
		return nil, environs.ErrNoInstances
	case len(result) < len(ids):
		return result, environs.ErrPartialInstances
	}
	return result, nil
}

// DeriveAvailabilityZones is part of the common.ZonedEnviron interface.
// A zone is derived from a "zone=<zone>" placement directive, or from the
// zone of any existing volumes that are to be attached to the instance.
func (env *azureEnviron) DeriveAvailabilityZones(
	ctx context.ProviderCallContext, args environs.StartInstanceParams,
) ([]string, error) {
	zone, err := env.deriveAvailabilityZone(ctx, args.Placement, args.VolumeAttachments)
	if err != nil {
		return nil, errors.Trace(err)
	}
	if zone == "" {
		return nil, nil
	}
	return []string{zone}, nil
}

// DistributeInstances implements the state.InstanceDistributor policy.
// Candidates are filtered to those in the least populated availability
// zones of the distribution group. If availability zones are not in use,
// all of the candidates are eligible.
func (env *azureEnviron) DistributeInstances(
	ctx context.ProviderCallContext,
	candidates, distributionGroup []instance.Id,
	limitZones []string,
) ([]instance.Id, error) {
	valid, err := common.DistributeInstances(
		env, ctx, candidates, distributionGroup, limitZones,
	)
	if errors.Is(err, errors.NotImplemented) {
		return candidates, nil
	}
	return valid, errors.Trace(err)
}

// deriveAvailabilityZone returns the availability zone implied by the
// placement directive and volume attachments, or "" if there is none.
func (env *azureEnviron) deriveAvailabilityZone(
	ctx context.ProviderCallContext,
	placement string,
	volumeAttachments []storage.VolumeAttachmentParams,
) (string, error) {
	var placementZone string
	if placement != "" {
		p, err := env.parsePlacement(placement)
		if err != nil {
			return "", errors.Trace(err)
		}
		placementZone = p.zone
	}
	if !env.useAvailabilityZones() {
		return "", nil
	}

	if placementZone != "" {
		zones, err := env.AvailabilityZones(ctx)
		if err != nil {
			return "", errors.Trace(err)
		}
		if err := zones.Validate(placementZone); err != nil {
			return "", errors.Trace(err)
		}
	}

	volumesZone, err := env.volumeAttachmentsZone(ctx, volumeAttachments)
	if err != nil {
		return "", errors.Trace(err)
	}
	if placementZone != "" && volumesZone != "" && placementZone != volumesZone {
		return "", errors.Errorf(
			"cannot create instance with placement %q, as this will prevent "+
				"attaching the requested disks in zone %q",
			placement, volumesZone,
		)
	}
	if placementZone != "" {
		return placementZone, nil
	}
	return volumesZone, nil
}

// volumeAttachmentsZone returns the availability zone of the disks to be
// attached, or "" if none of the disks is zonal. An error is returned if
// the disks are in different zones.
func (env *azureEnviron) volumeAttachmentsZone(
	ctx context.ProviderCallContext,
	attachments []storage.VolumeAttachmentParams,
) (string, error) {
	if len(attachments) == 0 {
		return "", nil
	}
	disks, err := env.disksClient()
	if err != nil {
		return "", errors.Trace(err)
	}
	var zone string
	for _, a := range attachments {
		if a.VolumeId == "" {
			continue
		}
		disk, err := disks.Get(ctx, env.resourceGroup, a.VolumeId, nil)
		if err != nil {
			return "", errorutils.HandleCredentialError(
				errors.Annotatef(err, "getting disk %q", a.VolumeId), ctx,
			)
		}
		if len(disk.Zones) == 0 {
			continue
		}
		diskZone := toValue(disk.Zones[0])
		if zone != "" && zone != diskZone {
			return "", errors.Errorf(
				"cannot attach disks from multiple availability zones: "+
					"%q is in zone %q, expected zone %q",
				a.VolumeId, diskZone, zone,
			)
		}
		zone = diskZone
	}
	return zone, nil
}
