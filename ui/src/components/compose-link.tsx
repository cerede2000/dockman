import {Typography} from "@mui/material";
import {useNavigate} from "react-router";
import {useHostStore} from "../pages/compose/state/files.ts";

// editorPathForStack is the one place this link's destination is built.
//
// It used to navigate to `/stacks/<path>`, a route that no longer exists: the
// editor lives under `/<host>/files/<path>`, which is what the Monitor's own
// "edit stack" action has always used. Every stack link in the Deploy tab and
// the Volumes page therefore led nowhere.
//
// ?tab=0 pins the EDITOR tab regardless of the compose.defaultTab setting, the
// same choice the Monitor makes: following a stack link means "show me its
// file", not "reopen whatever tab it was left on".
// eslint-disable-next-line react-refresh/only-export-components
export function editorPathForStack(host: string, servicePath: string): string | null {
    if (!host || !servicePath) return null
    return `/${host}/files/${servicePath}?tab=0`
}

const ComposeLink = ({servicePath, stackName}: { servicePath: string; stackName: string; }) => {
    const navigate = useNavigate()
    const host = useHostStore(state => state.host)
    const destination = editorPathForStack(host, servicePath)

    // The server sends an empty path when a container's compose file is outside
    // every configured alias, or when it was never started by Compose at all.
    // Such a stack has no file Dockman can open, so it is named, not linked -
    // a link to `/<host>/files/` would only land on the file index.
    if (destination === null) {
        return (
            <Typography variant="body2" component="span" sx={{wordBreak: 'break-all', color: 'text.secondary'}}>
                {stackName}
            </Typography>
        );
    }

    return (
        <Typography
            variant="body2"
            component="span"
            role="link"
            sx={{
                textDecoration: 'none',
                color: 'primary.main',
                wordBreak: 'break-all',
                cursor: 'pointer',
                '&:hover': {textDecoration: 'underline'}
            }}
            onClick={(event) => {
                event.stopPropagation() // Prevent row click
                navigate(destination)
            }}
        >
            {stackName}
        </Typography>
    );
};

export default ComposeLink;
