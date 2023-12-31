package lib

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
	"strconv"

	"github.com/litmuschaos/litmus-go/pkg/cerrors"
	"github.com/palantir/stacktrace"
	"github.com/sirupsen/logrus"

	clients "github.com/litmuschaos/litmus-go/pkg/clients"
	"github.com/litmuschaos/litmus-go/pkg/events"
	experimentTypes "github.com/litmuschaos/litmus-go/pkg/kubernetes/node-restart/types"
	"github.com/litmuschaos/litmus-go/pkg/log"
	"github.com/litmuschaos/litmus-go/pkg/probe"
	"github.com/litmuschaos/litmus-go/pkg/status"
	"github.com/litmuschaos/litmus-go/pkg/types"
	"github.com/litmuschaos/litmus-go/pkg/utils/common"
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

	nodesAffectedPerc, _ := strconv.Atoi(experimentsDetails.NodesAffectedPerc)
	targetNodeList, err := common.GetNodeList(experimentsDetails.TargetNode, experimentsDetails.NodeLabel, nodesAffectedPerc, clients)
	if err != nil {
		return stacktrace.Propagate(err, "could not get node list")
	}

	log.InfoWithValues("[Info]: Details of Nodes under chaos injection", logrus.Fields{
		"No. Of Nodes": len(targetNodeList),
		"Node Names":   targetNodeList,
	})

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
	if err := restartNode(targetNodeList, experimentsDetails, clients, chaosDetails); err != nil {
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
func restartNode(targetNodeList []string, experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets, chaosDetails *types.ChaosDetails) error {

	select {
	case <-inject:
		// stopping the chaos execution, if abort signal received
		os.Exit(0)
	default:
		token, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
		if err != nil {
			return err
		}
		setClusterCmd := exec.Command("kubectl", "config", "set-cluster", "kubernetes", "--certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt", "--server=https://kubernetes.default.svc")
		if err := common.RunCLICommands(setClusterCmd, "", "", "failed to set cluster configuration", cerrors.ErrorTypeHelper); err != nil {
			return err
		}

		// Set credentials
		setCredentialsCmd := exec.Command("kubectl", "config", "set-credentials", "sa", "--token", string(token))
		if err := common.RunCLICommands(setCredentialsCmd, "", "", "failed to set credentials", cerrors.ErrorTypeHelper); err != nil {
			return err
		}

		// Set context
		setContextCmd := exec.Command("kubectl", "config", "set-context", "default", "--cluster", "kubernetes", "--user=sa")
		if err := common.RunCLICommands(setContextCmd, "", "", "failed to set context", cerrors.ErrorTypeHelper); err != nil {
			return err
		}

		// Use context
		useContextCmd := exec.Command("kubectl", "config", "use-context", "default")
		if err := common.RunCLICommands(useContextCmd, "", "", "failed to use context", cerrors.ErrorTypeHelper); err != nil {
			return err
		}
		for _, appNode := range targetNodeList {
			log.Infof("[Inject]: Restarting the %v node", appNode)
			command := exec.Command("kubectl", "node_shell", appNode, "--", "shutdown", "-r", "+1")
			if err := common.RunCLICommands(command, "", fmt.Sprintf("{node: %s}", appNode), "failed to restart the target node", cerrors.ErrorTypeChaosInject); err != nil {
				return err
			}
	
			common.SetTargets(appNode, "injected", "node", chaosDetails)
	
		}


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