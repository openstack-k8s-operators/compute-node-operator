#!/bin/bash
#
# Copyright 2020 Red Hat Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License"); you may
# not use this file except in compliance with the License. You may obtain
# a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
# WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
# License for the specific language governing permissions and limitations
# under the License.

set -ex

OS_COMPUTE_API_VERSION="2.latest"

if [ -z "${SCALE_DOWN_NODE_NAME}" ] ; then
  echo "No host to scale down provides!"
  exit 1
fi

# Get compute ID from service list
COMPUTE_SERVICE_ID=$(openstack compute service list -f value -c ID \
                      --host ${SCALE_DOWN_NODE_NAME})

# Delete nova-compute from registered nova services
while [ -n "${COMPUTE_SERVICE_ID}" ]; do
  openstack compute service delete ${COMPUTE_SERVICE_ID}
  sleep 5
  COMPUTE_SERVICE_ID=$(openstack compute service list -f value -c ID \
                        --host ${SCALE_DOWN_NODE_NAME})
done

# TODO: mschuppert - delete neutron OVN controller when supported

# TODO: mschuppert - delete stale allocations from resource provider, otherwise delete
#       of resource provider will fail
# openstack resource provider delete 5361d775-7f4a-4f5d-aacc-03f8eeea881b
# Unable to delete resource provider 5361d775-7f4a-4f5d-aacc-03f8eeea881b: Resource provider has allocations. (HTTP 409)

# Delete nova-compute as a placement resource provider
PLACEMENT_PROVIDER_ID=$(openstack resource provider list -f value \
                          -c uuid --name ${SCALE_DOWN_NODE_NAME})
while [ -n "${PLACEMENT_PROVIDER_ID}" ]; do
  openstack resource provider delete ${PLACEMENT_PROVIDER_ID}
  sleep 5
  PLACEMENT_PROVIDER_ID=$(openstack resource provider list -f value \
                            -c uuid --name ${SCALE_DOWN_NODE_NAME})
done

exit 0
