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

if [ -z "${DISABLE_COMPUTE_SERVICES}" ] ; then
  echo "No compute host provided to disable nova-compute service!"
  exit 1
fi

# Disable all compute worker ndoes to not have the nova-scheduler
# to pick a node which is going to be removed next.
for COMPUTE_SERVICE in ${DISABLE_COMPUTE_SERVICES}; do
  # Disable compute service
  COMPUTE_SERVICE_STATUS=$(openstack compute service list --long \
                          --service nova-compute \
                          --host ${COMPUTE_SERVICE} \
                          -f value -c Status)

  while [ "${COMPUTE_SERVICE_STATUS}" = "enabled" ]; do
    openstack compute service set --disable \
      --disable-reason "scaling down: ${COMPUTE_SERVICE}" \
      ${COMPUTE_SERVICE} nova-compute
    sleep 2
    COMPUTE_SERVICE_STATUS=$(openstack compute service list --long \
                              --service nova-compute \
                              --host ${COMPUTE_SERVICE} \
                              -f value -c Status)
  done

  openstack compute service list \
    --long --service nova-compute --host ${COMPUTE_SERVICE}
done

exit 0
