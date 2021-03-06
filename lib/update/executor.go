/*
Copyright 2018 Gravitational, Inc.

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

package update

import (
	"github.com/gravitational/gravity/lib/fsm"
	"github.com/gravitational/gravity/lib/ops"

	"github.com/gravitational/trace"
)

const (
	// updateInit is the phase to initialize the update operation
	updateInit = "update_init"
	// updateChecks is the phase to run preflight checks
	updateChecks = "update_checks"
	// updateBootstrap is the phase to bootstrap cluster update operation
	updateBootstrap = "update_bootstrap"
	// updateSystem is the phase to update system software on nodes
	updateSystem = "update_system"
	// preUpdate is the phase to run pre-update application hook
	preUpdate = "pre_update"
	// coredns is a phase to create coredns related roles
	coredns = "coredns"
	// updateApp is the phase to update the application
	updateApp = "update_app"
	// electionStatus is the phase to control node leader elections
	electionStatus = "election_status"
	// taintNode is the phase to taint a node
	taintNode = "taint_node"
	// untaintNode is the phase to remove the node taint
	untaintNode = "untaint_node"
	// drainNode is the phase to drain a node
	drainNode = "drain_node"
	// uncordonNode is the phase to uncordon a node
	uncordonNode = "uncordon_node"
	// endpoints is the phase to wait for system service endpoints
	endpoints = "endpoints"
	// config is the phase that updates system configuration
	config = "config"
	// kubeletPermissions is the phase to add kubelet permissions
	kubeletPermissions = "kubelet_permissions"
	// migrateLinks is the phase to migrate links to trusted clusters
	migrateLinks = "links"
	// updateLabels is the phase to update node labels in the cluster
	updateLabels = "labels"
	// migrateRoles is the phase to migrate roles to a new format
	migrateRoles = "roles"
	// updateEtcdBackup is the phase to backup the etcd datastore before upgrade
	updateEtcdBackup = "etcd_backup"
	// updateEtcdShutdown is the phase to shutdown the etcd datastore for upgrade
	updateEtcdShutdown = "etcd_shutdown"
	// updateEtcdMaster is the phase to upgrade the leader (first) etcd server
	updateEtcdMaster = "etcd_upgrade"
	// updateEtcdRestore is the phase to restore the etcd data to the new etcd instance
	updateEtcdRestore = "etcd_restore"
	// updateEtcdRestart is the phase that restarts etcd service to listen on regular ports
	updateEtcdRestart = "etcd_restart"
	// updateEtcdRestartGravity is the phase that restarts gravity-site
	updateEtcdRestartGravity = "etcd_restart_gravity"
	// cleanupNode is the phase to clean up a node after the upgrade
	cleanupNode = "cleanup_node"
)

// fsmSpec returns the function that returns an appropriate phase executor
func fsmSpec(c FSMConfig) fsm.FSMSpecFunc {
	return func(p fsm.ExecutorParams, remote fsm.Remote) (fsm.PhaseExecutor, error) {
		if p.Phase.Executor == "" {
			return nil, trace.BadParameter("error in plan, executor for phase %q was not specified", p.Phase.ID)
		}
		if p.Plan.OperationType != ops.OperationUpdate {
			return nil, trace.BadParameter("unsupported operation %q", p.Plan.OperationType)
		}

		switch p.Phase.Executor {
		case updateInit:
			return NewUpdatePhaseInit(c, p.Plan, p.Phase)
		case updateChecks:
			return NewUpdatePhaseChecks(c, p.Plan, p.Phase, c.Remote)
		case updateBootstrap:
			return NewUpdatePhaseBootstrap(c, p.Plan, p.Phase, remote)
		case coredns:
			return NewPhaseCoreDNS(c, p.Plan, p.Phase)
		case updateSystem:
			return NewUpdatePhaseSystem(c, p.Plan, p.Phase, remote)
		case preUpdate:
			return NewUpdatePhaseBeforeApp(c, p.Plan, p.Phase)
		case updateApp:
			return NewUpdatePhaseApp(c, p.Plan, p.Phase)
		case electionStatus:
			return NewPhaseElectionChange(p.Plan, p.Phase, remote, c.Operator)
		case taintNode:
			return NewPhaseTaint(c, p.Plan, p.Phase)
		case untaintNode:
			return NewPhaseUntaint(c, p.Plan, p.Phase)
		case drainNode:
			return NewPhaseDrain(c, p.Plan, p.Phase)
		case uncordonNode:
			return NewPhaseUncordon(c, p.Plan, p.Phase)
		case endpoints:
			return NewPhaseEndpoints(c, p.Plan, p.Phase)
		case config:
			return NewUpdatePhaseConfig(c, p, remote)
		case kubeletPermissions:
			return NewPhaseKubeletPermissions(c, p.Plan, p.Phase)
		case migrateLinks:
			return NewPhaseMigrateLinks(c, p.Plan, p.Phase)
		case updateLabels:
			return NewPhaseUpdateLabels(c, p.Plan, p.Phase)
		case migrateRoles:
			return NewPhaseMigrateRoles(c, p.Plan, p.Phase)
		case updateEtcdBackup:
			return NewPhaseUpgradeEtcdBackup(c, p.Plan, p.Phase)
		case updateEtcdShutdown:
			return NewPhaseUpgradeEtcdShutdown(c, p.Plan, p.Phase)
		case updateEtcdMaster:
			return NewPhaseUpgradeEtcd(c, p.Plan, p.Phase)
		case updateEtcdRestore:
			return NewPhaseUpgradeEtcdRestore(c, p.Plan, p.Phase)
		case updateEtcdRestart:
			return NewPhaseUpgradeEtcdRestart(c, p.Plan, p.Phase)
		case updateEtcdRestartGravity:
			return NewPhaseUpgradeGravitySiteRestart(c, p.Plan, p.Phase)
		case cleanupNode:
			return NewGarbageCollectPhase(p.Plan, p.Phase, remote)
		default:
			return nil, trace.BadParameter(
				"phase %q requires executor %q (potential mismatch between upgrade versions)",
				p.Phase.ID, p.Phase.Executor)
		}
	}
}
