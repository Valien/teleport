/*
Copyright 2017-2019 Gravitational, Inc.

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

package services

import (
	"github.com/gravitational/trace"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/utils"
)

// UnmarshalClusterName unmarshals the ClusterName resource from JSON.
func UnmarshalClusterName(bytes []byte, opts ...MarshalOption) (types.ClusterName, error) {
	var clusterName types.ClusterNameV2

	if len(bytes) == 0 {
		return nil, trace.BadParameter("missing resource data")
	}

	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := utils.FastUnmarshal(bytes, &clusterName); err != nil {
		return nil, trace.BadParameter(err.Error())
	}

	err = clusterName.CheckAndSetDefaults()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if cfg.ID != 0 {
		clusterName.SetResourceID(cfg.ID)
	}
	if !cfg.Expires.IsZero() {
		clusterName.SetExpiry(cfg.Expires)
	}

	return &clusterName, nil
}

// MarshalClusterName marshals the ClusterName resource to JSON.
func MarshalClusterName(clusterName types.ClusterName, opts ...MarshalOption) ([]byte, error) {
	cfg, err := CollectOptions(opts)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	switch clusterName := clusterName.(type) {
	case *types.ClusterNameV2:
		if version := clusterName.GetVersion(); version != types.V2 {
			return nil, trace.BadParameter("mismatched cluster name version %v and type %T", version, clusterName)
		}
		if !cfg.PreserveResourceID {
			// avoid modifying the original object
			// to prevent unexpected data races
			copy := *clusterName
			copy.SetResourceID(0)
			clusterName = &copy
		}
		return utils.FastMarshal(clusterName)
	default:
		return nil, trace.BadParameter("unrecognized cluster name version %T", clusterName)
	}
}
