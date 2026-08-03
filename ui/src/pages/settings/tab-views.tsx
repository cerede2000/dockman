import {Box, Button, FormControlLabel, Paper, Stack, Switch, Typography} from '@mui/material';
import {BarChart, Inventory2Outlined, OpenInNew} from '@mui/icons-material';
import {useNavigate} from 'react-router';
import {useHostStore} from '../compose/state/files.ts';
import {useNavigationPreferences} from '../home/navigation-preferences.ts';

export default function TabViews() {
    const navigate = useNavigate();
    const host = useHostStore(state => state.host) || 'local';
    const showStats = useNavigationPreferences(state => state.showStats);
    const showContainers = useNavigationPreferences(state => state.showContainers);
    const setShowStats = useNavigationPreferences(state => state.setShowStats);
    const setShowContainers = useNavigationPreferences(state => state.setShowContainers);

    const legacyViews = [
        {
            title: 'Stats',
            description: 'Legacy host and container statistics view.',
            path: `/${host}/stats`,
            visible: showStats,
            setVisible: setShowStats,
            icon: <BarChart color="primary"/>,
        },
        {
            title: 'Containers',
            description: 'Legacy flat container management view.',
            path: `/${host}/containers`,
            visible: showContainers,
            setVisible: setShowContainers,
            icon: <Inventory2Outlined color="primary"/>,
        },
    ];

    return (
        <Box sx={{maxWidth: 900, mx: 'auto', p: {xs: 2, md: 4}}}>
            <Typography variant="h5" gutterBottom>Views</Typography>
            <Typography color="text.secondary" sx={{mb: 3}}>
                Monitor replaces the legacy Stats and Containers shortcuts. The views remain available and can be restored to the sidebar at any time.
            </Typography>
            <Stack spacing={1.5}>
                {legacyViews.map(view => (
                    <Paper key={view.title} variant="outlined" sx={{p: 2}}>
                        <Stack direction={{xs: 'column', sm: 'row'}} spacing={2} sx={{alignItems: {sm: 'center'}}}>
                            {view.icon}
                            <Box sx={{flex: 1}}>
                                <Typography variant="subtitle1">{view.title}</Typography>
                                <Typography variant="body2" color="text.secondary">{view.description}</Typography>
                            </Box>
                            <FormControlLabel
                                control={<Switch checked={view.visible} onChange={(_, checked) => view.setVisible(checked)}/>}
                                label="Show in sidebar"
                            />
                            <Button variant="outlined" startIcon={<OpenInNew/>} onClick={() => navigate(view.path)}>
                                Open
                            </Button>
                        </Stack>
                    </Paper>
                ))}
            </Stack>
        </Box>
    );
}
