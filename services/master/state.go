package master

import (
	"github.com/sdslabs/gasper/lib/mongo"
	"github.com/sdslabs/gasper/lib/utils"
	"github.com/sdslabs/gasper/types"
)

// updateHostIP updates the application's host IP address
func updateHostIP(oldIP, currentIP string) (interface{}, error) {
	return mongo.UpdateInstances(
		types.M{
			mongo.HostIPKey: oldIP,
		},
		types.M{
			mongo.HostIPKey: currentIP,
		},
	)
}

// updateState updates the IP address of the machine in the application's context
// and re-registers all the microservices and applications deployed
func updateState(currentIP string) {
	utils.LogInfo(
		"Master-State-1",
		"IP address of the machine changed from %s to %s",
		utils.HostIP,
		currentIP)

	_, err := updateHostIP(utils.HostIP, currentIP)
	if err != nil {
		utils.LogError("Master-State-2", err)
		return
	}
	utils.HostIP = currentIP
}

// checkAndUpdateState checks whether the IP address of the machine has changed or not
func checkAndUpdateState(currentIP string) {
	if utils.HostIP != currentIP {
		updateState(currentIP)
	}
}
