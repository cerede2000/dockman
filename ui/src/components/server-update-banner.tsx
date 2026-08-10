import {Alert, Button, Slide, Snackbar} from '@mui/material';
import {useServerBuildChanged} from '../hooks/app-build.ts';

/**
 * Announces that the server now serves a different build than the one this
 * page is running.
 *
 * Reloading is left to the user on purpose: an automatic reload would discard
 * an unsaved compose file mid-edit. The banner does not auto-dismiss either -
 * the page stays out of date until it is reloaded, so hiding the notice would
 * only hide the problem.
 */
export default function ServerUpdateBanner() {
    const changed = useServerBuildChanged();
    if (!changed) return null;
    return (
        <Snackbar
            open
            anchorOrigin={{vertical: 'bottom', horizontal: 'center'}}
            slots={{transition: Slide}}
        >
            <Alert
                severity="info"
                variant="filled"
                action={<Button color="inherit" size="small" onClick={() => window.location.reload()}>Reload</Button>}
            >
                Dockman was updated. Reload to get the new interface.
            </Alert>
        </Snackbar>
    );
}
