import {Box} from '@mui/material';
import {useDockerStats} from "../../hooks/docker-containers-stats.ts";
import {ContainerStatTable} from './components/container-stat-table.tsx';
import AggregateStats from "./components/container-stat-chart.tsx";
import {statsTheme} from "./components/stats-theme.ts";

interface StackStatsProps {
    selectedPage?: string;
}

export function TabStat({selectedPage = ""}: StackStatsProps) {
    const {containers, history, pollInfo, loading, handleSortChange, sortOrder, sortField} = useDockerStats(selectedPage)

    // diagnostic: history depth of the first row, shown next to the poll
    // counter so chart accumulation is verifiable at a glance
    const firstWithHistory = containers.find(c => history.has(c.id));
    const historyPoints = firstWithHistory ? (history.get(firstWithHistory.id)?.cpu.length ?? 0) : 0;

    return (
        <Box sx={{
            p: 1,
            height: '100vh',
            display: 'flex',
            flexDirection: 'column',
            overflow: 'hidden',
            boxSizing: 'border-box',
            bgcolor: statsTheme.page,
        }}>
            <Box sx={{flexShrink: 0}}>
                <AggregateStats
                    containers={containers}
                    loading={loading}
                    pollInfo={pollInfo}
                    historyPoints={historyPoints}
                />
            </Box>

            <Box sx={{flexGrow: 1, minHeight: 0}}>
                <ContainerStatTable
                    loading={loading}
                    containers={containers}
                    history={history}
                    activeSortField={sortField}
                    order={sortOrder}
                    onFieldClick={handleSortChange}
                />
            </Box>
        </Box>
    );
}