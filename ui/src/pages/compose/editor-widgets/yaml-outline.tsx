import {Box, Button, Divider, Stack, Typography} from "@mui/material";
import {
    AccountTreeOutlined,
    HubOutlined,
    KeyOutlined,
    StorageOutlined,
    ViewInArOutlined,
} from "@mui/icons-material";
import type {YamlOutlineItem, YamlOutlineSection} from "../components/yaml-outline.ts";
import type {ReactNode} from "react";

const sectionIcons: Record<YamlOutlineSection, ReactNode> = {
    services: <ViewInArOutlined fontSize="small"/>,
    networks: <HubOutlined fontSize="small"/>,
    volumes: <StorageOutlined fontSize="small"/>,
    secrets: <KeyOutlined fontSize="small"/>,
};

interface YamlOutlineWidgetProps {
    items: YamlOutlineItem[];
    onNavigate: (item: YamlOutlineItem) => void;
}

const YamlOutlineWidget = ({items, onNavigate}: YamlOutlineWidgetProps) => {
    if (items.length === 0) {
        return (
            <Stack spacing={1.5} sx={{pt: 2, color: 'text.secondary', textAlign: 'center', alignItems: 'center'}}>
                <AccountTreeOutlined/>
                <Typography variant="body2">
                    No services, networks, volumes or secrets found.
                </Typography>
            </Stack>
        );
    }

    return (
        <Box sx={{height: '100%', overflowY: 'auto', pr: 0.5}}>
            <Typography variant="subtitle2" sx={{mb: 1}}>Compose outline</Typography>
            <Stack spacing={0.5}>
                {items.map((item, index) => (
                    <Box key={`${item.section}-${item.line}-${item.name}`}>
                        {item.level === 0 && index > 0 && <Divider sx={{my: 0.5}}/>}
                        <Button
                            fullWidth
                            size="small"
                            color="inherit"
                            onClick={() => onNavigate(item)}
                            startIcon={item.level === 0 ? sectionIcons[item.section] : undefined}
                            sx={{
                                minHeight: 30,
                                justifyContent: 'flex-start',
                                pl: item.level === 0 ? 1 : 4,
                                textTransform: 'none',
                                fontWeight: item.level === 0 ? 700 : 400,
                                color: item.level === 0 ? 'text.primary' : 'text.secondary',
                                '&:hover': {color: 'text.primary'},
                            }}
                        >
                            <Box component="span" sx={{overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>
                                {item.name}
                            </Box>
                            <Typography component="span" variant="caption" sx={{ml: 'auto', pl: 1, opacity: 0.6}}>
                                {item.line}
                            </Typography>
                        </Button>
                    </Box>
                ))}
            </Stack>
        </Box>
    );
};

export default YamlOutlineWidget;
