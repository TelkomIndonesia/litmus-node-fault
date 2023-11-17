package lib

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/litmuschaos/litmus-go/pkg/cerrors"
	"github.com/palantir/stacktrace"

	clients "github.com/litmuschaos/litmus-go/pkg/clients"
	"github.com/litmuschaos/litmus-go/pkg/events"
	experimentTypes "github.com/litmuschaos/litmus-go/pkg/kubernetes/node-restart/types"
	"github.com/litmuschaos/litmus-go/pkg/log"
	"github.com/litmuschaos/litmus-go/pkg/probe"
	"github.com/litmuschaos/litmus-go/pkg/status"
	"github.com/litmuschaos/litmus-go/pkg/types"
	"github.com/litmuschaos/litmus-go/pkg/utils/common"
	"github.com/litmuschaos/litmus-go/pkg/utils/retry"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	err           error
	inject, abort chan os.Signal
)

// PrepareNodeRestart contains the preparation steps before chaos injection
func PrepareNodeRestart(experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets, resultDetails *types.ResultDetails, eventsDetails *types.EventDetails, chaosDetails *types.ChaosDetails) error {

	// inject channel is used to transmit signal notifications.
	inject = make(chan os.Signal, 1)
	// Catch and relay certain signal(s) to inject channel.
	signal.Notify(inject, os.Interrupt, syscall.SIGTERM)

	// abort channel is used to transmit signal notifications.
	abort = make(chan os.Signal, 1)
	// Catch and relay certain signal(s) to abort channel.
	signal.Notify(abort, os.Interrupt, syscall.SIGTERM)

	//Waiting for the ramp time before chaos injection
	if experimentsDetails.RampTime != 0 {
		log.Infof("[Ramp]: Waiting for the %vs ramp time before injecting chaos", experimentsDetails.RampTime)
		common.WaitForDuration(experimentsDetails.RampTime)
	}

	if experimentsDetails.TargetNode == "" {
		//Select node for kubelet-service-kill
		experimentsDetails.TargetNode, err = common.GetNodeName(experimentsDetails.AppNS, experimentsDetails.AppLabel, experimentsDetails.NodeLabel, clients)
		if err != nil {
			return stacktrace.Propagate(err, "could not get node name")
		}
	}

	if experimentsDetails.EngineName != "" {
		msg := "Injecting " + experimentsDetails.ExperimentName + " chaos on " + experimentsDetails.TargetNode + " node"
		types.SetEngineEventAttributes(eventsDetails, types.ChaosInject, msg, "Normal", chaosDetails)
		events.GenerateEvents(eventsDetails, clients, chaosDetails, "ChaosEngine")
	}

	// run the probes during chaos
	if len(resultDetails.ProbeDetails) != 0 {
		if err = probe.RunProbes(chaosDetails, clients, resultDetails, "DuringChaos", eventsDetails); err != nil {
			return err
		}
	}

	// watching for the abort signal and revert the chaos
	go abortWatcher(experimentsDetails, clients, resultDetails, chaosDetails, eventsDetails)

	// Restart the application node
	if err := restartNode(experimentsDetails, clients, chaosDetails); err != nil {
		log.Info("[Revert]: Reverting chaos because error during restart of node")
		return stacktrace.Propagate(err, "could not restart node")
	}

	// Verify the status of AUT after reschedule
	log.Info("[Status]: Verify the status of AUT after reschedule")
	if err = status.AUTStatusCheck(clients, chaosDetails); err != nil {
		log.Info("[Revert]: Reverting chaos because application status check failed")
		return err
	}

	// Verify the status of Auxiliary Applications after reschedule
	if experimentsDetails.AuxiliaryAppInfo != "" {
		log.Info("[Status]: Verify that the Auxiliary Applications are running")
		if err = status.CheckAuxiliaryApplicationStatus(experimentsDetails.AuxiliaryAppInfo, experimentsDetails.Timeout, experimentsDetails.Delay, clients); err != nil {
			log.Info("[Revert]: Reverting chaos because auxiliary application status check failed")
			return err
		}
	}

	log.Infof("[Chaos]: Waiting for %vs", experimentsDetails.ChaosDuration)

	common.WaitForDuration(experimentsDetails.ChaosDuration)

	log.Info("[Chaos]: Stopping the experiment")

	//Waiting for the ramp time after chaos injection
	if experimentsDetails.RampTime != 0 {
		log.Infof("[Ramp]: Waiting for the %vs ramp time after injecting chaos", experimentsDetails.RampTime)
		common.WaitForDuration(experimentsDetails.RampTime)
	}
	return nil
}

// restartNode restart the target node
func restartNode(experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets, chaosDetails *types.ChaosDetails) error {

	select {
	case <-inject:
		// stopping the chaos execution, if abort signal received
		os.Exit(0)
	default:
		log.Infof("[Inject]: Restarting the %v node", experimentsDetails.TargetNode)
		exec.Command("kubectl", "config", "set-cluster", "kubernetes", "--certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt", "--server=https://kubernetes.default.svc")
		exec.Command("kubectl", "config", "set-credentials", "sa", "--token", "$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)")
		exec.Command("kubectl", "config", "set-context", "default", "--cluster", "kubernetes", "--user=sa")
		exec.Command("kubectl", "config", "use-context", "default")
		command := exec.Command("kubectl", "node_shell", experimentsDetails.TargetNode, "--", "shutdown", "-r", "+3")
		if err := common.RunCLICommands(command, "", fmt.Sprintf("{node: %s}", experimentsDetails.TargetNode), "failed to restart the target node", cerrors.ErrorTypeChaosInject); err != nil {
			return err
		}

		common.SetTargets(experimentsDetails.TargetNode, "injected", "node", chaosDetails)

		return retry.
			Times(uint(experimentsDetails.Timeout / experimentsDetails.Delay)).
			Wait(time.Duration(experimentsDetails.Delay) * time.Second).
			Try(func(attempt uint) error {
				nodeSpec, err := clients.KubeClient.CoreV1().Nodes().Get(context.Background(), experimentsDetails.TargetNode, v1.GetOptions{})
				if err != nil {
					return cerrors.Error{ErrorCode: cerrors.ErrorTypeChaosInject, Target: fmt.Sprintf("{node: %s}", experimentsDetails.TargetNode), Reason: err.Error()}
				}
				if !nodeSpec.Spec.Unschedulable {
					return cerrors.Error{ErrorCode: cerrors.ErrorTypeChaosInject, Target: fmt.Sprintf("{node: %s}", experimentsDetails.TargetNode), Reason: "node is not in unschedule state"}
				}
				return nil
			})
	}
	return nil
}

// abortWatcher continuously watch for the abort signals
func abortWatcher(experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets, resultDetails *types.ResultDetails, chaosDetails *types.ChaosDetails, eventsDetails *types.EventDetails) {
	// waiting till the abort signal received
	<-abort

	log.Info("[Chaos]: Killing process started because of terminated signal received")
	log.Info("Chaos Revert Started")
	// retry thrice for the chaos revert
	retry := 3
	for retry > 0 {
		retry--
		time.Sleep(1 * time.Second)
	}
	log.Info("Chaos Revert Completed")
	os.Exit(0)
}
