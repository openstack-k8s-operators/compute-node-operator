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
  echo "No host to scale down provided!"
  exit 1
fi

if [ -z "${DISABLE_COMPUTE_SERVICES}" ] ; then
  echo "No compute host provided to disable nova-compute service!"
  exit 1
fi

# Disable compute services
# Disable all compute worker nodes to not have the nova-scheduler
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

# get all instances of the host which are ACTIVE
# This does _NOT_ handle instances in SHUTOFF, ERROR or any other
# state than ACTIVE
INSTANCES=$(openstack server list --long --all-projects -f value \
              -c ID --status ACTIVE --host ${SCALE_DOWN_NODE_NAME})

# Live migrate ACTIVE instances off the compute node if enabled
if [ -n "${INSTANCES}" ] && [ "${LIVE_MIGRATION_ENABLED}" = true ]; then
  echo "Trying to migrate instances with IDs: ${INSTANCES}"
  for i in ${INSTANCES} ; do
    # using nova client as openstack client does not support block
    # migration auto-detection
    echo "Trigger migration of instance with ID: ${i}"
    nova live-migration ${i}
    sleep 2
  done
fi

# wait until all instances are gone from compute node.
# We'll wait until either all instances got migrated, either
# automatically or manual.
INSTANCES_ON_WORKER=$(openstack server list --long --all-projects \
                        -f value -c ID \
                        --host ${SCALE_DOWN_NODE_NAME})
while [ -n "${INSTANCES_ON_WORKER}" ]; do
  echo "Still VMs on ${SCALE_DOWN_NODE_NAME}: ${INSTANCES_ON_WORKER}"
  sleep 30
  INSTANCES_ON_WORKER=$(openstack server list --long --all-projects \
                          -f value -c ID \
                          --host ${SCALE_DOWN_NODE_NAME})
done

exit 0
