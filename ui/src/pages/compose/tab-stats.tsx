import {Box} from '@mui/material';
import {useMemo} from 'react';
import {useDockerStats} from "../../hooks/docker-containers-stats.ts";
import {ContainerStatTable} from './components/container-stat-table.tsx';
import AggregateStats from "./components/container-stat-chart.tsx";
import {statsTheme} from "./components/stats-theme.ts";
import useSearch from "../../hooks/search.ts";
import SearchBar from "../../components/search-bar.tsx";

interface StackStatsProps {
    selectedPage?: string;
}

export function TabStat({selectedPage = ""}: StackStatsProps) {
    const {containers, history, aggregates, loading, handleSortChange, sortOrder, sortField} = useDockerStats(selectedPage)
    const {search, setSearch, searchInputRef} = useSearch()

    // display-only filter: the aggregate band keeps totalling everything
    const filteredContainers = useMemo(() => {
        const query = search.trim().toLowerCase();
        if (!query) return containers;
        return containers.filter(c => c.name.toLowerCase().includes(query));
    }, [containers, search]);

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
                <AggregateStats aggregates={aggregates}/>
            </Box>

            <Box sx={{display: 'flex', justifyContent: 'flex-end', mb: 1, flexShrink: 0}}>
                <SearchBar search={search} setSearch={setSearch} inputRef={searchInputRef}/>
            </Box>

            <Box sx={{flexGrow: 1, minHeight: 0}}>
                <ContainerStatTable
                    loading={loading}
                    containers={filteredContainers}
                    history={history}
                    activeSortField={sortField}
                    order={sortOrder}
                    onFieldClick={handleSortChange}
                />
            </Box>
        </Box>
    );
}
