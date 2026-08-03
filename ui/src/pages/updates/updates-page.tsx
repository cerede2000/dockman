import {Box, Button, Paper, Stack, Typography} from '@mui/material';
import {SpaceDashboardOutlined, SystemUpdateAlt} from '@mui/icons-material';
import {useNavigate} from 'react-router';
import {useHostFromUrl} from '../home/home.tsx';

export default function UpdatesPage() {
    const host = useHostFromUrl();
    const navigate = useNavigate();

    return (
        <Box sx={{p: {xs: 2, md: 3}, maxWidth: 1000, mx: 'auto'}}>
            <Stack direction="row" spacing={1.5} sx={{mb: 3, alignItems: 'center'}}>
                <SystemUpdateAlt color="primary" sx={{fontSize: 32}}/>
                <Box>
                    <Typography variant="h4">Updates</Typography>
                    <Typography color="text.secondary">Container image update policies and activity</Typography>
                </Box>
            </Stack>
            <Paper variant="outlined" sx={{p: 3}}>
                <Typography variant="h6" gutterBottom>Automatic updates</Typography>
                <Typography color="text.secondary" sx={{mb: 2}}>
                    Policy configuration will be introduced in the next implementation lot. On-demand image checks and protected updates remain available from Monitor.
                </Typography>
                <Button startIcon={<SpaceDashboardOutlined/>} variant="contained" onClick={() => navigate(`/${host}/monitor`)}>
                    Open Monitor
                </Button>
            </Paper>
        </Box>
    );
}
