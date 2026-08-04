package docker

import (
	"testing"

	"github.com/RA341/dockman/internal/docker/updater"
	"github.com/stretchr/testify/require"
)

func TestGroupAutomaticUpdateTargetsKeepsStackAtomicAndContainersIndependent(t *testing.T) {
	stackKey := "demo|/stacks/demo/compose.yml"
	units := groupAutomaticUpdateTargets([]updater.UpdateExecutionTarget{
		{ContainerID: "worker", ContainerName: "worker", TargetType: updater.UpdateTargetStack, StackName: "demo", StackKey: stackKey},
		{ContainerID: "standalone", ContainerName: "standalone", TargetType: updater.UpdateTargetContainer},
		{ContainerID: "db", ContainerName: "db", TargetType: updater.UpdateTargetStack, StackName: "demo", StackKey: stackKey},
	})

	require.Len(t, units, 2)
	require.False(t, units[0].transactional)
	require.Equal(t, "standalone", units[0].targets[0].ContainerName)
	require.True(t, units[1].transactional)
	require.Equal(t, "demo", units[1].stackName)
	require.ElementsMatch(t, []string{"worker", "db"}, []string{units[1].targets[0].ContainerName, units[1].targets[1].ContainerName})
}

func TestOrderStackUpdateTargetsHonoursComposeDependencies(t *testing.T) {
	targets := []updater.UpdateExecutionTarget{
		{ContainerName: "front", ServiceName: "front", DependsOn: "api:service_healthy:false"},
		{ContainerName: "db", ServiceName: "db"},
		{ContainerName: "api", ServiceName: "api", DependsOn: "db:service_started:false"},
		{ContainerName: "metrics", ServiceName: "metrics", DependsOn: "external:service_started:false"},
	}
	ordered := orderStackUpdateTargets(targets)
	names := make([]string, 0, len(ordered))
	for _, target := range ordered {
		names = append(names, target.ServiceName)
	}
	require.Less(t, slicesIndex(names, "db"), slicesIndex(names, "api"))
	require.Less(t, slicesIndex(names, "api"), slicesIndex(names, "front"))
}

func TestOrderStackUpdateTargetsBreaksCyclesDeterministically(t *testing.T) {
	ordered := orderStackUpdateTargets([]updater.UpdateExecutionTarget{
		{ContainerName: "z", ServiceName: "z", DependsOn: "a:service_started:false"},
		{ContainerName: "a", ServiceName: "a", DependsOn: "z:service_started:false"},
	})
	require.Equal(t, []string{"a", "z"}, []string{ordered[0].ContainerName, ordered[1].ContainerName})
}

func slicesIndex(values []string, wanted string) int {
	for index, value := range values {
		if value == wanted {
			return index
		}
	}
	return -1
}
