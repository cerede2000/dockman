import useButtonAction from "../hooks/button-action.ts";
import React from "react";
import {Button, CircularProgress, Stack, Tooltip} from "@mui/material";

interface Action {
    action: string;
    buttonText: string;
    icon: React.ReactElement,
    disabled: boolean;
    handler: () => Promise<void>;
    tooltip: string;
}

interface ActionButtonProps {
    actions: Action[];
    variant?: 'outlined' | 'contained'
}

// one compact action row shared by every list view (containers, images,
// volumes, networks): small buttons, sentence case, quiet borders that pick
// up the accent on hover — same recipe as the deploy tab's action row
function ActionButtons({actions, variant = 'outlined'}: ActionButtonProps) {
    const {buttonAction, activeAction} = useButtonAction()

    return (
        <Stack direction="row" spacing={1} alignItems="center">
            {actions.map((action) => (
                <Tooltip key={action.action} title={action.tooltip}>
                    <span>
                        <Button
                            variant={variant}
                            size="small"
                            onClick={() => buttonAction(action.handler, action.action)}
                            disabled={action.disabled || !!activeAction}
                            startIcon={activeAction === action.action ?
                                <CircularProgress size={15} color="inherit"/> :
                                action.icon
                            }
                            sx={{
                                textTransform: 'none',
                                fontWeight: 600,
                                px: 1.5,
                                borderColor: 'divider',
                                color: 'text.secondary',
                                whiteSpace: 'nowrap',
                                '&:hover': {
                                    borderColor: 'primary.main',
                                    color: 'primary.main',
                                    bgcolor: 'action.hover',
                                },
                                '& .MuiButton-startIcon svg': {fontSize: 17},
                            }}
                        >
                            {action.buttonText}
                        </Button>
                    </span>
                </Tooltip>
            ))}
        </Stack>
    );
}

export default ActionButtons;
