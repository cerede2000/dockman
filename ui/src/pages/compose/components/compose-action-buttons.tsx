import {useHostClient} from "../../../lib/api.ts";
import {useFileComponents} from "../state/terminal.tsx";
import {Box, Button, ButtonGroup, CircularProgress} from "@mui/material";
import {deployActionsConfig, useComposeAction} from "../state/compose.tsx";
import {DockerService} from "../../../gen/docker/v1/docker_pb.ts";

export function ComposeActionHeaders({selectedServices, fetchContainers}: {
    selectedServices: string[];
    fetchContainers: () => Promise<void>
}) {
    const dockerService = useHostClient(DockerService);

    const runAction = useComposeAction(state => state.runAction)
    const activeAction = useComposeAction(state => state.activeAction)
    const {filename} = useFileComponents();
    const composeFile = filename!

    const handleComposeAction = (
        name: typeof deployActionsConfig[number]['name'],
        _message: string,
        rpcName: typeof deployActionsConfig[number]['rpcName'],
    ) => {
        runAction(
            composeFile,
            dockerService[rpcName],
            name,
            selectedServices,
            () => fetchContainers()
        )
    };

    return (
        <Box sx={{display: 'flex', gap: 1.5, flexWrap: 'wrap', mb: 2, flexShrink: 0, alignItems: 'center'}}>
            <ButtonGroup
                variant="outlined"
                size="small"
                sx={{
                    '& .MuiButton-root': {
                        textTransform: 'none',
                        fontWeight: 600,
                        px: 1.5,
                        borderColor: 'divider',
                        color: 'text.secondary',
                        '&:hover': {
                            borderColor: 'primary.main',
                            color: 'primary.main',
                            bgcolor: 'action.hover',
                        },
                    },
                    '& .MuiButton-startIcon svg': {fontSize: 17},
                }}
            >
                {deployActionsConfig.map((action) => (
                    <Button
                        key={action.name}
                        disabled={!!activeAction}
                        onClick={() => handleComposeAction(action.name, action.message, action.rpcName)}
                        startIcon={
                            activeAction === action.name ?
                                <CircularProgress size={15} color="inherit"/> :
                                action.icon
                        }
                    >
                        {action.name.charAt(0).toUpperCase() + action.name.slice(1)}
                    </Button>
                ))}
            </ButtonGroup>
        </Box>
    )
}
