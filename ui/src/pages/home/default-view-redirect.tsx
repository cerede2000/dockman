import {useEffect, useState} from 'react';
import {Navigate} from 'react-router';
import {useConfig} from '../../hooks/config.ts';
import {resolveLandingView, useNavigationPreferences} from './navigation-preferences.ts';

// The index route of a host. A choice made in this browser applies at once;
// otherwise the redirect waits briefly for dockman.yml so its defaultView
// applies on a cold load, then falls back to Files if it never arrives.
export default function HostDefaultViewRedirect() {
    const preferred = useNavigationPreferences(state => state.defaultView);
    const {dockYaml} = useConfig();
    const [waited, setWaited] = useState(false);

    useEffect(() => {
        if (preferred) return;
        const id = setTimeout(() => setWaited(true), 1500);
        return () => clearTimeout(id);
    }, [preferred]);

    if (!preferred && !dockYaml && !waited) return null;
    return <Navigate to={resolveLandingView(preferred, dockYaml?.defaultView)} replace/>;
}
