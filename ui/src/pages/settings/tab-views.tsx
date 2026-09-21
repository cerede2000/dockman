import {
    Box,
    Button,
    Chip,
    FormControlLabel,
    IconButton,
    MenuItem,
    Paper,
    Stack,
    Switch,
    TextField,
    Tooltip,
    Typography
} from '@mui/material';
import {ArrowDownward, ArrowUpward, OpenInNew, RestartAlt} from '@mui/icons-material';
import {useNavigate} from 'react-router';
import {useConfig} from '../../hooks/config.ts';
import {useHostStore} from '../compose/state/files.ts';
import {
    isShownIn,
    moveOrder,
    NAV_VIEW_IDS,
    type NavViewId,
    resolveLandingView,
    useNavigationPreferences,
    visibleViews
} from '../home/navigation-preferences.ts';
import {NAV_VIEWS} from '../home/navigation-views.tsx';

// The legacy views Monitor replaced: hidden from the sidebar by default, still
// reachable, and the only entries that can be switched off.
const LEGACY_VIEWS: Partial<Record<NavViewId, string>> = {
    stats: 'Legacy host and container statistics view.',
    containers: 'Legacy flat container management view.',
};

export default function TabViews() {
    const navigate = useNavigate();
    const host = useHostStore(state => state.host) || 'local';
    const {dockYaml} = useConfig();

    const order = useNavigationPreferences(state => state.order);
    const showStats = useNavigationPreferences(state => state.showStats);
    const showContainers = useNavigationPreferences(state => state.showContainers);
    const defaultView = useNavigationPreferences(state => state.defaultView);
    const setShowStats = useNavigationPreferences(state => state.setShowStats);
    const setShowContainers = useNavigationPreferences(state => state.setShowContainers);
    const moveView = useNavigationPreferences(state => state.moveView);
    const resetOrder = useNavigationPreferences(state => state.resetOrder);
    const setDefaultView = useNavigationPreferences(state => state.setDefaultView);

    const shown = visibleViews(order, {showStats, showContainers});
    const isShown = isShownIn({showStats, showContainers});
    // a button is enabled only when its move would change the order
    const canMove = (id: NavViewId, offset: -1 | 1) => moveOrder(order, id, offset, isShown) !== order;
    const isDefaultOrder = order.every((id, index) => id === NAV_VIEW_IDS[index]);
    const serverDefault = NAV_VIEWS[resolveLandingView('', dockYaml?.defaultView)].title;

    const visibility: Partial<Record<NavViewId, { visible: boolean; setVisible: (v: boolean) => void }>> = {
        stats: {visible: showStats, setVisible: setShowStats},
        containers: {visible: showContainers, setVisible: setShowContainers},
    };

    return (
        <Box sx={{maxWidth: 900, mx: 'auto', p: {xs: 2, md: 4}}}>
            <Typography variant="h5" gutterBottom>Views</Typography>
            <Typography color="text.secondary" sx={{mb: 3}}>
                These preferences are stored in this browser, like the rest of this page. They change nothing for
                other users.
            </Typography>

            <Typography variant="subtitle1" sx={{fontWeight: 600, mb: 1}}>Landing page</Typography>
            <Paper variant="outlined" sx={{p: 2, mb: 4}}>
                <TextField
                    select
                    fullWidth
                    size="small"
                    label="Open Dockman on"
                    value={defaultView}
                    onChange={event => setDefaultView(event.target.value as NavViewId | '')}
                    helperText={`The server default comes from defaultView in dockman.yml; a choice here takes precedence in this browser.`}
                    // '' is a real choice ("Server default"): show it, and keep
                    // the label above the field instead of over an empty box
                    slotProps={{inputLabel: {shrink: true}, select: {displayEmpty: true}}}
                >
                    <MenuItem value="">Server default ({serverDefault})</MenuItem>
                    {NAV_VIEW_IDS.map(id => (
                        <MenuItem key={id} value={id}>{NAV_VIEWS[id].title}</MenuItem>
                    ))}
                </TextField>
            </Paper>

            <Stack direction="row" sx={{alignItems: 'center', mb: 1}}>
                <Typography variant="subtitle1" sx={{fontWeight: 600, flexGrow: 1}}>Sidebar</Typography>
                <Button size="small" startIcon={<RestartAlt/>} disabled={isDefaultOrder} onClick={resetOrder}>
                    Reset order
                </Button>
            </Stack>
            <Typography variant="body2" color="text.secondary" sx={{mb: 1.5}}>
                Order the sidebar entries. Alt + 1 to 9 opens the entry at that position.
            </Typography>
            <Stack spacing={1} component="ol" aria-label="Sidebar order" sx={{p: 0, m: 0, listStyle: 'none'}}>
                {order.map(id => {
                    const view = NAV_VIEWS[id];
                    const Icon = view.icon;
                    const position = shown.indexOf(id);
                    const toggle = visibility[id];
                    const description = LEGACY_VIEWS[id];
                    return (
                        <Paper key={id} component="li" variant="outlined" aria-label={view.title}
                               sx={{px: 2, py: 1, opacity: position < 0 ? 0.6 : 1}}>
                            <Stack direction={{xs: 'column', sm: 'row'}} spacing={2}
                                   sx={{alignItems: {sm: 'center'}}}>
                                <Box sx={{width: 64, flexShrink: 0}}>
                                    {position < 0
                                        ? <Chip size="small" variant="outlined" label="Hidden"/>
                                        : position < 9
                                            ? <Chip size="small" label={`Alt+${position + 1}`}/>
                                            : null}
                                </Box>
                                <Box sx={{display: 'flex', width: 24, justifyContent: 'center'}}><Icon/></Box>
                                <Box sx={{flex: 1, minWidth: 0}}>
                                    <Typography variant="subtitle2">{view.title}</Typography>
                                    {description && (
                                        <Typography variant="body2" color="text.secondary">{description}</Typography>
                                    )}
                                </Box>
                                {toggle && (
                                    <FormControlLabel
                                        control={<Switch checked={toggle.visible}
                                                         onChange={(_, checked) => toggle.setVisible(checked)}/>}
                                        label="Show in sidebar"
                                    />
                                )}
                                {toggle && (
                                    <Button size="small" variant="outlined" startIcon={<OpenInNew/>}
                                            onClick={() => navigate(`/${host}/${id}`)}>
                                        Open
                                    </Button>
                                )}
                                <Stack direction="row">
                                    <Tooltip title="Move up">
                                        <span>
                                            <IconButton size="small" aria-label={`Move ${view.title} up`}
                                                        disabled={!canMove(id, -1)}
                                                        onClick={() => moveView(id, -1)}>
                                                <ArrowUpward fontSize="small"/>
                                            </IconButton>
                                        </span>
                                    </Tooltip>
                                    <Tooltip title="Move down">
                                        <span>
                                            <IconButton size="small" aria-label={`Move ${view.title} down`}
                                                        disabled={!canMove(id, 1)}
                                                        onClick={() => moveView(id, 1)}>
                                                <ArrowDownward fontSize="small"/>
                                            </IconButton>
                                        </span>
                                    </Tooltip>
                                </Stack>
                            </Stack>
                        </Paper>
                    );
                })}
            </Stack>
        </Box>
    );
}
