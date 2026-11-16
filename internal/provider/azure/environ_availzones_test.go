// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package azure_test

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/juju/errors"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	corebase "github.com/juju/juju/core/base"
	"github.com/juju/juju/core/constraints"
	"github.com/juju/juju/core/instance"
	corenetwork "github.com/juju/juju/core/network"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/context"
	"github.com/juju/juju/environs/tags"
	envtesting "github.com/juju/juju/environs/testing"
	"github.com/juju/juju/internal/provider/azure/internal/azuretesting"
	"github.com/juju/juju/internal/provider/common"
	"github.com/juju/juju/storage"
	"github.com/juju/juju/testing"
)

var useAvailabilityZones = testing.Attrs{"use-availability-zones": true}

// setSKUZones sets the availability zones in which each of the test
// instance types is offered.
func (s *environSuite) setSKUZones() {
	skuZones := map[string][]string{
		"Standard_A1":      {"1", "2"},
		"Standard_D1":      {"1", "2", "3"},
		"Standard_D2":      {"1", "2", "3"},
		"Standard_D8ps_v5": {"1"},
	}
	for _, sku := range s.skus {
		zones, ok := skuZones[toValue(sku.Name)]
		if !ok {
			continue
		}
		sku.LocationInfo = []*armcompute.ResourceSKULocationInfo{{
			Location: to.Ptr("westus"),
			Zones:    to.SliceOfPtrs(zones...),
		}, {
			// Zones for other locations must be ignored.
			Location: to.Ptr("eastus"),
			Zones:    to.SliceOfPtrs("4"),
		}}
	}
}

func (s *environSuite) zonedEnviron(c *gc.C, attrs ...testing.Attrs) common.ZonedEnviron {
	env := s.openEnviron(c, attrs...)
	zonedEnv, ok := env.(common.ZonedEnviron)
	c.Assert(ok, jc.IsTrue)
	return zonedEnv
}

func (s *environSuite) TestAvailabilityZonesNotEnabled(c *gc.C) {
	env := s.zonedEnviron(c)
	_, err := env.AvailabilityZones(s.callCtx)
	c.Assert(err, jc.ErrorIs, errors.NotImplemented)
	c.Check(s.requests, gc.HasLen, 0)
}

func (s *environSuite) TestAvailabilityZones(c *gc.C) {
	s.setSKUZones()
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{s.resourceSKUsSender()}

	zones, err := env.AvailabilityZones(s.callCtx)
	c.Assert(err, jc.ErrorIsNil)
	names := make([]string, len(zones))
	for i, z := range zones {
		names[i] = z.Name()
		c.Check(z.Available(), jc.IsTrue)
	}
	c.Check(names, jc.DeepEquals, []string{"1", "2", "3"})
}

func (s *environSuite) TestAvailabilityZonesNoneInLocation(c *gc.C) {
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{s.resourceSKUsSender()}

	_, err := env.AvailabilityZones(s.callCtx)
	c.Assert(err, jc.ErrorIs, errors.NotImplemented)
}

func (s *environSuite) TestInstanceAvailabilityZoneNamesNotEnabled(c *gc.C) {
	env := s.zonedEnviron(c)
	_, err := env.InstanceAvailabilityZoneNames(
		s.callCtx, []instance.Id{"juju-06f00d-0"},
	)
	c.Assert(err, jc.ErrorIs, errors.NotImplemented)
	c.Check(s.requests, gc.HasLen, 0)
}

func (s *environSuite) TestInstanceAvailabilityZoneNames(c *gc.C) {
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{makeSender(
		".*/virtualMachines", armcompute.VirtualMachineListResult{
			Value: []*armcompute.VirtualMachine{{
				Name:  to.Ptr("juju-06f00d-0"),
				Zones: to.SliceOfPtrs("2"),
			}, {
				Name: to.Ptr("juju-06f00d-1"),
			}},
		},
	)}

	zones, err := env.InstanceAvailabilityZoneNames(s.callCtx, []instance.Id{
		"juju-06f00d-0", "juju-06f00d-1", "juju-06f00d-2",
	})
	c.Assert(err, gc.Equals, environs.ErrPartialInstances)
	c.Check(zones, jc.DeepEquals, map[instance.Id]string{
		"juju-06f00d-0": "2",
		"juju-06f00d-1": "",
	})
}

func (s *environSuite) TestInstanceAvailabilityZoneNamesNoInstances(c *gc.C) {
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{makeSender(
		".*/virtualMachines", armcompute.VirtualMachineListResult{},
	)}

	_, err := env.InstanceAvailabilityZoneNames(
		s.callCtx, []instance.Id{"juju-06f00d-0"},
	)
	c.Assert(err, gc.Equals, environs.ErrNoInstances)
}

func (s *environSuite) TestDeriveAvailabilityZonesNoPlacement(c *gc.C) {
	env := s.zonedEnviron(c, useAvailabilityZones)
	zones, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{},
	)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(zones, gc.HasLen, 0)
	c.Check(s.requests, gc.HasLen, 0)
}

func (s *environSuite) TestDeriveAvailabilityZonesPlacement(c *gc.C) {
	s.setSKUZones()
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{s.resourceSKUsSender()}

	zones, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{Placement: "zone=2"},
	)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(zones, jc.DeepEquals, []string{"2"})
}

func (s *environSuite) TestDeriveAvailabilityZonesInvalidPlacementZone(c *gc.C) {
	s.setSKUZones()
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{s.resourceSKUsSender()}

	_, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{Placement: "zone=4"},
	)
	c.Assert(err, gc.ErrorMatches, `availability zone "4" not valid`)
}

func (s *environSuite) TestDeriveAvailabilityZonesPlacementNotEnabled(c *gc.C) {
	env := s.zonedEnviron(c)
	_, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{Placement: "zone=2"},
	)
	c.Assert(err, jc.ErrorIs, errors.NotSupported)
	c.Assert(err, gc.ErrorMatches,
		`zone placement without use-availability-zones=true not supported`)
}

func (s *environSuite) TestDeriveAvailabilityZonesNotEnabledIgnoresVolumes(c *gc.C) {
	env := s.zonedEnviron(c)
	zones, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{
			VolumeAttachments: []storage.VolumeAttachmentParams{{
				VolumeId: "volume-0",
			}},
		},
	)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(zones, gc.HasLen, 0)
	c.Check(s.requests, gc.HasLen, 0)
}

func (s *environSuite) TestDeriveAvailabilityZonesVolumeAttachments(c *gc.C) {
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{
		makeSender(".*/disks/volume-0", &armcompute.Disk{
			Name: to.Ptr("volume-0"),
		}),
		makeSender(".*/disks/volume-1", &armcompute.Disk{
			Name:  to.Ptr("volume-1"),
			Zones: to.SliceOfPtrs("3"),
		}),
	}

	zones, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{
			VolumeAttachments: []storage.VolumeAttachmentParams{{
				VolumeId: "volume-0",
			}, {
				VolumeId: "volume-1",
			}},
		},
	)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(zones, jc.DeepEquals, []string{"3"})
}

func (s *environSuite) TestDeriveAvailabilityZonesVolumeAttachmentsConflict(c *gc.C) {
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{
		makeSender(".*/disks/volume-0", &armcompute.Disk{
			Name:  to.Ptr("volume-0"),
			Zones: to.SliceOfPtrs("1"),
		}),
		makeSender(".*/disks/volume-1", &armcompute.Disk{
			Name:  to.Ptr("volume-1"),
			Zones: to.SliceOfPtrs("3"),
		}),
	}

	_, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{
			VolumeAttachments: []storage.VolumeAttachmentParams{{
				VolumeId: "volume-0",
			}, {
				VolumeId: "volume-1",
			}},
		},
	)
	c.Assert(err, gc.ErrorMatches, `cannot attach disks from multiple availability zones: `+
		`"volume-1" is in zone "3", expected zone "1"`)
}

func (s *environSuite) TestDeriveAvailabilityZonesPlacementVolumeConflict(c *gc.C) {
	s.setSKUZones()
	env := s.zonedEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{
		s.resourceSKUsSender(),
		makeSender(".*/disks/volume-0", &armcompute.Disk{
			Name:  to.Ptr("volume-0"),
			Zones: to.SliceOfPtrs("3"),
		}),
	}

	_, err := env.DeriveAvailabilityZones(
		s.callCtx, environs.StartInstanceParams{
			Placement: "zone=1",
			VolumeAttachments: []storage.VolumeAttachmentParams{{
				VolumeId: "volume-0",
			}},
		},
	)
	c.Assert(err, gc.ErrorMatches, `cannot create instance with placement "zone=1", `+
		`as this will prevent attaching the requested disks in zone "3"`)
}

func (s *environSuite) TestDistributeInstancesNotEnabled(c *gc.C) {
	env := s.openEnviron(c)
	distributor, ok := env.(context.Distributor)
	c.Assert(ok, jc.IsTrue)

	candidates := []instance.Id{"juju-06f00d-0", "juju-06f00d-1"}
	eligible, err := distributor.DistributeInstances(
		s.callCtx, candidates, []instance.Id{"juju-06f00d-2"}, nil,
	)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(eligible, jc.DeepEquals, candidates)
	c.Check(s.requests, gc.HasLen, 0)
}

func (s *environSuite) TestDistributeInstances(c *gc.C) {
	s.setSKUZones()
	env := s.openEnviron(c, useAvailabilityZones)
	distributor, ok := env.(context.Distributor)
	c.Assert(ok, jc.IsTrue)
	s.sender = azuretesting.Senders{
		makeSender(".*/virtualMachines", armcompute.VirtualMachineListResult{
			Value: []*armcompute.VirtualMachine{{
				Name:  to.Ptr("juju-06f00d-0"),
				Zones: to.SliceOfPtrs("1"),
			}, {
				Name:  to.Ptr("juju-06f00d-1"),
				Zones: to.SliceOfPtrs("1"),
			}, {
				Name:  to.Ptr("juju-06f00d-2"),
				Zones: to.SliceOfPtrs("2"),
			}, {
				Name:  to.Ptr("juju-06f00d-3"),
				Zones: to.SliceOfPtrs("3"),
			}},
		}),
		s.resourceSKUsSender(),
	}

	// Zone 1 has two instances in the distribution group, so the
	// instances in zones 2 and 3 are in the least populated zones.
	group := []instance.Id{
		"juju-06f00d-0", "juju-06f00d-1", "juju-06f00d-2", "juju-06f00d-3",
	}
	candidates := []instance.Id{
		"juju-06f00d-2", "juju-06f00d-3", "juju-06f00d-9",
	}
	eligible, err := distributor.DistributeInstances(
		s.callCtx, candidates, group, nil,
	)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(eligible, jc.SameContents, []instance.Id{
		"juju-06f00d-2", "juju-06f00d-3",
	})
}

func (s *environSuite) TestBootstrapAvailabilityZone(c *gc.C) {
	defer envtesting.DisableFinishBootstrap()()
	s.setSKUZones()

	ctx := envtesting.BootstrapTODOContext(c)
	env := prepareForBootstrap(c, ctx, s.provider, &s.sender, useAvailabilityZones)

	s.sender = s.initResourceGroupSenders(resourceGroupName)
	s.sender = append(s.sender, s.startInstanceSenders(c, startInstanceSenderParams{bootstrap: true})...)
	s.requests = nil
	_, err := env.Bootstrap(
		ctx, s.callCtx, environs.BootstrapParams{
			ControllerConfig:        testing.FakeControllerConfig(),
			AvailableTools:          makeToolsList("ubuntu"),
			BootstrapBase:           corebase.MustParseBaseFromString("ubuntu@22.04"),
			BootstrapConstraints:    constraints.MustParse("mem=3.5G zones=2,3"),
			SupportedBootstrapBases: testing.FakeSupportedJujuBases,
		},
	)
	c.Assert(err, jc.ErrorIsNil)

	// The controller is created in the first zone that matches the
	// bootstrap constraints, and does not join an availability set.
	c.Assert(len(s.requests), gc.Equals, numExpectedBootstrapStartInstanceRequests)
	s.vmTags[tags.JujuIsController] = "true"
	s.assertStartInstanceRequests(c, s.requests[1:], assertStartInstanceRequestsParams{
		bootstrap:        true,
		imageReference:   &jammyImageReferenceGen2,
		diskSizeGB:       32,
		osProfile:        &s.linuxOsProfile,
		instanceType:     "Standard_D1",
		publicIP:         true,
		availabilityZone: "2",
	})
}

func (s *environSuite) TestPrecheckInstanceZonePlacementNotEnabled(c *gc.C) {
	env := s.openEnviron(c)
	err := env.PrecheckInstance(s.callCtx, environs.PrecheckInstanceParams{
		Base:      corebase.MakeDefaultBase("ubuntu", "22.04"),
		Placement: "zone=1",
	})
	c.Assert(err, jc.ErrorIs, errors.NotSupported)
}

func (s *environSuite) TestPrecheckInstanceZonePlacement(c *gc.C) {
	s.setSKUZones()
	env := s.openEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{s.resourceSKUsSender()}

	err := env.PrecheckInstance(s.callCtx, environs.PrecheckInstanceParams{
		Base:      corebase.MakeDefaultBase("ubuntu", "22.04"),
		Placement: "zone=3",
	})
	c.Assert(err, jc.ErrorIsNil)

	err = env.PrecheckInstance(s.callCtx, environs.PrecheckInstanceParams{
		Base:      corebase.MakeDefaultBase("ubuntu", "22.04"),
		Placement: "zone=4",
	})
	c.Assert(err, gc.ErrorMatches, `availability zone "4" not valid`)
}

func (s *environSuite) TestStartInstanceAvailabilityZone(c *gc.C) {
	s.setSKUZones()
	env := s.openEnviron(c, useAvailabilityZones)
	// The machine hosts a unit, but must not join an availability set.
	s.vmTags[tags.JujuUnitsDeployed] = "mysql/0"
	s.sender = s.startInstanceSenders(c, startInstanceSenderParams{bootstrap: false})
	s.requests = nil
	params := makeStartInstanceParams(c, s.controllerUUID, corebase.MakeDefaultBase("ubuntu", "22.04"))
	params.InstanceConfig.Tags[tags.JujuUnitsDeployed] = "mysql/0"
	params.AvailabilityZone = "2"

	result, err := env.StartInstance(s.callCtx, params)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(result.Hardware.AvailabilityZone, gc.NotNil)
	c.Check(*result.Hardware.AvailabilityZone, gc.Equals, "2")
	s.assertStartInstanceRequests(c, s.requests, assertStartInstanceRequestsParams{
		imageReference:   &jammyImageReferenceGen2,
		diskSizeGB:       32,
		osProfile:        &s.linuxOsProfile,
		instanceType:     "Standard_A1",
		publicIP:         true,
		availabilityZone: "2",
	})
}

func (s *environSuite) TestStartInstanceAvailabilityZoneFiltersInstanceTypes(c *gc.C) {
	s.setSKUZones()
	env := s.openEnviron(c, useAvailabilityZones)
	s.sender = s.startInstanceSenders(c, startInstanceSenderParams{bootstrap: false})
	s.requests = nil
	params := makeStartInstanceParams(c, s.controllerUUID, corebase.MakeDefaultBase("ubuntu", "22.04"))
	// Standard_A1 is not offered in zone 3.
	params.AvailabilityZone = "3"

	result, err := env.StartInstance(s.callCtx, params)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(*result.Hardware.AvailabilityZone, gc.Equals, "3")
	s.assertStartInstanceRequests(c, s.requests, assertStartInstanceRequestsParams{
		imageReference:   &jammyImageReferenceGen2,
		diskSizeGB:       32,
		osProfile:        &s.linuxOsProfile,
		instanceType:     "Standard_D1",
		publicIP:         true,
		availabilityZone: "3",
	})
}

func (s *environSuite) TestStartInstanceAvailabilityZoneNoInstanceTypes(c *gc.C) {
	s.setSKUZones()
	env := s.openEnviron(c, useAvailabilityZones)
	s.sender = azuretesting.Senders{s.resourceSKUsSender()}
	params := makeStartInstanceParams(c, s.controllerUUID, corebase.MakeDefaultBase("ubuntu", "22.04"))
	params.AvailabilityZone = "4"

	_, err := env.StartInstance(s.callCtx, params)
	c.Assert(err, gc.ErrorMatches,
		`no instance types available in availability zone "4"`)
	// The error must not be zone independent, so that the
	// provisioner tries another zone.
	c.Check(errors.Is(err, environs.ErrAvailabilityZoneIndependent), jc.IsFalse)
}

func (s *environSuite) TestStartInstanceAvailabilityZoneNotEnabled(c *gc.C) {
	s.setSKUZones()
	env := s.openEnviron(c)
	s.vmTags[tags.JujuUnitsDeployed] = "mysql/0"
	s.sender = s.startInstanceSenders(c, startInstanceSenderParams{bootstrap: false})
	s.requests = nil
	params := makeStartInstanceParams(c, s.controllerUUID, corebase.MakeDefaultBase("ubuntu", "22.04"))
	params.InstanceConfig.Tags[tags.JujuUnitsDeployed] = "mysql/0"
	params.AvailabilityZone = "2"

	result, err := env.StartInstance(s.callCtx, params)
	c.Assert(err, jc.ErrorIsNil)
	c.Check(result.Hardware.AvailabilityZone, gc.IsNil)
	s.assertStartInstanceRequests(c, s.requests, assertStartInstanceRequestsParams{
		availabilitySetName: "mysql",
		imageReference:      &jammyImageReferenceGen2,
		diskSizeGB:          32,
		osProfile:           &s.linuxOsProfile,
		instanceType:        "Standard_A1",
		publicIP:            true,
	})
}

func (s *environSuite) TestStartInstanceAvailabilityZoneWithSpaceConstraints(c *gc.C) {
	s.setSKUZones()
	env := s.openEnviron(c, useAvailabilityZones)
	s.sender = s.startInstanceSenders(c, startInstanceSenderParams{
		bootstrap: false, hasSpaceConstraints: true,
	})
	s.requests = nil
	params := makeStartInstanceParams(c, s.controllerUUID, corebase.MakeDefaultBase("ubuntu", "22.04"))
	params.Constraints.Spaces = &[]string{"foo", "bar"}
	// Azure subnets are regional, so have no zones.
	params.SubnetsToZones = []map[corenetwork.Id][]string{
		{"/path/to/subnet1": nil},
		{"/path/to/subnet2": nil},
	}
	params.AvailabilityZone = "1"

	_, err := env.StartInstance(s.callCtx, params)
	c.Assert(err, jc.ErrorIsNil)
	s.assertStartInstanceRequests(c, s.requests, assertStartInstanceRequestsParams{
		imageReference:      &jammyImageReferenceGen2,
		diskSizeGB:          32,
		osProfile:           &s.linuxOsProfile,
		instanceType:        "Standard_A1",
		publicIP:            true,
		subnets:             []string{"/path/to/subnet1", "/path/to/subnet2"},
		hasSpaceConstraints: true,
		availabilityZone:    "1",
	})
}
