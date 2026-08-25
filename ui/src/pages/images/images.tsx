import {useMemo, useState} from 'react';
import {Alert, Box, Button, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, Divider, Fade, Link, Paper, Stack, Typography} from '@mui/material';
import {CleaningServices, Delete, Sanitizer, Storage} from '@mui/icons-material';
import PageHeader, {RefreshButton} from "../../components/page-header.tsx";
import {useHostStore} from "../compose/state/files.ts";
import {ImageTable} from './images-table.tsx';
import {formatBytes} from "../../lib/editor.ts";
import scrollbarStyles from "../../components/scrollbar-style.tsx";
import useSearch from "../../hooks/search.ts";
import ActionButtons from "../../components/action-buttons.tsx";
import SearchBar from "../../components/search-bar.tsx";
import {useDockerImages} from "./docker-images.ts";

const ImagesPage = () => {
    const {
        images,
        loading,
        refreshImages,
        pruneUnused,
        totalImageSize,
        unusedContainerCount,
        untagged,
        deleteImages
    } = useDockerImages();

    const {search, setSearch, searchInputRef} = useSearch();
    const [selectedImages, setSelectedImages] = useState<string[]>([])
    // Pruning used to run on the click, with nothing said about what it takes
    // with it. An automatic update leaves the version it replaced behind as an
    // UNTAGGED image, which is exactly what both of these buttons target: the
    // rollback safety net and the prune target are the same objects.
    const [pruneScope, setPruneScope] = useState<'untagged' | 'unused' | null>(null)
    const host = useHostStore(state => state.host)

    const filteredImages = useMemo(() => {
        if (search) {
            // untagged (dangling) images have no repoTags at all — indexing
            // [0] blindly crashed the whole page on the first keystroke.
            // Match any tag or the id, case-insensitively.
            const query = search.toLowerCase();
            return images.filter(image =>
                image.repoTags.some(tag => tag.toLowerCase().includes(query)) ||
                image.id.toLowerCase().includes(query))
        }
        return images;
    }, [images, search]);

    const actions = [
        {
            action: 'deleteSelected',
            buttonText: `Delete ${selectedImages.length === 0 ? "" : `${selectedImages.length}`} images`,
            icon: <Delete/>,
            disabled: loading || selectedImages.length === 0,
            handler: async () => {
                await deleteImages(selectedImages)
                setSelectedImages([])
            },
            tooltip: 'Delete selected images',
        },
        {
            action: 'deleteUntagged',
            buttonText: `Prune Untagged (${untagged})`,
            icon: <Sanitizer/>,
            disabled: loading,
            handler: async () => {
                setPruneScope('untagged')
            },
            tooltip: 'Delete Untagged images',
        },
        {
            action: 'deleteUnused',
            buttonText: `Prune Unused (${unusedContainerCount})`,
            tooltip: 'Delete all unused images',
            icon: <CleaningServices/>,
            disabled: loading,
            handler: async () => {
                setPruneScope('unused')
            },
        }
    ]

    const confirmPrune = async () => {
        const scope = pruneScope
        setPruneScope(null)
        if (scope === null) return
        await pruneUnused(scope === 'unused')
    }

    return (
        <Box sx={{
            display: 'flex',
            flexDirection: 'column',
            height: '100vh',
            p: 3,
            overflow: 'hidden',
            ...scrollbarStyles
        }}>
            <PageHeader
                icon={<Storage/>}
                title="Images"
                count={images.length}
                extra={formatBytes(totalImageSize) ?? '0B'}
                host={host}
            />

            <Paper
                variant="outlined"
                sx={{
                    px: 1.5,
                    py: 1,
                    mb: 1.5,
                    display: 'flex',
                    alignItems: 'center',
                    gap: 1.5,
                    borderRadius: 2,
                    flexShrink: 0,
                    boxShadow: '0 2px 4px rgba(0,0,0,0.02)'
                }}
            >
                <Box sx={{flex: 1, maxWidth: 270}}>
                    <SearchBar search={search} setSearch={setSearch} inputRef={searchInputRef}/>
                </Box>

                <Divider orientation="vertical" flexItem sx={{mx: 0.5}}/>

                <Box sx={{display: 'flex', alignItems: 'center', gap: 1.5, flex: 1}}>
                    <ActionButtons actions={actions}/>
                    <RefreshButton onClick={refreshImages} loading={loading}/>
                </Box>
            </Paper>

            {/* Table Container */}
            <Box sx={{
                flexGrow: 1,
                border: '1px solid',
                borderColor: 'divider',
                borderRadius: 2,
                display: 'flex',
                flexDirection: 'column',
                overflow: 'hidden',
                minHeight: 0
            }}>
                {loading ? (
                    <ImagesLoading/>
                ) : (
                    <Fade in={!loading} timeout={300}>
                        <Box sx={{
                            width: '100%',
                            height: '100%',
                            overflowY: 'auto',
                            display: 'flex',
                            flexDirection: 'column'
                        }}>
                            {images.length === 0 ? (
                                <ImagesEmpty searchTerm={''}/>
                            ) : (
                                <ImageTable
                                    images={filteredImages}
                                    selectedImages={selectedImages}
                                    onSelectionChange={setSelectedImages}
                                />
                            )}
                        </Box>
                    </Fade>
                )}
            </Box>

            <Dialog open={pruneScope !== null} onClose={() => setPruneScope(null)} maxWidth="sm" fullWidth>
                <DialogTitle sx={{display: 'flex', alignItems: 'center', gap: 1}}>
                    <Sanitizer color="warning"/>
                    {pruneScope === 'unused' ? 'Prune all unused images' : 'Prune untagged images'}
                </DialogTitle>
                <DialogContent>
                    <Stack spacing={2} sx={{pt: 1}}>
                        <Alert severity="warning">
                            This also removes the images automatic updates keep in order to roll back.
                            When an update replaces a container, the version it replaced loses its tag
                            and stays behind as an untagged image &mdash; which is exactly what this
                            removes. Rolling an updated container back to its previous version will no
                            longer be possible.
                        </Alert>
                        {pruneScope === 'unused' && (
                            <Alert severity="info">
                                &ldquo;Unused&rdquo; goes further than untagged: every image no container
                                currently uses is removed, tagged or not, and will have to be pulled again.
                            </Alert>
                        )}
                        <Typography variant="body2" color="text.secondary">
                            Nothing a running or stopped container depends on is affected.
                        </Typography>
                    </Stack>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => setPruneScope(null)}>Cancel</Button>
                    <Button variant="contained" color="warning" onClick={() => void confirmPrune()}>
                        {pruneScope === 'unused' ? 'Prune unused images' : 'Prune untagged images'}
                    </Button>
                </DialogActions>
            </Dialog>
        </Box>
    )
};

const ImagesLoading = () => {
    return (
        <Box sx={{
            display: 'flex',
            justifyContent: 'center',
            alignItems: 'center',
            width: '100%',
            flex: 1
        }}>
            <CircularProgress sx={{mr: 2}}/>
            <Typography variant="body1" sx={{
                color: "text.secondary"
            }}>
                Loading images...
            </Typography>
        </Box>
    );
};


const ImagesEmpty = ({searchTerm}: { searchTerm: string }) => {
    return (
        <Paper sx={{
            p: 6,
            textAlign: 'center',
            height: '100%',
            width: '100%',
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'center'
        }}>
            <Storage sx={{
                fontSize: 48,
                color: 'text.secondary',
                mb: 2,
                mx: 'auto'
            }}/>
            <Typography variant="h6" sx={{mb: 1}}>
                {searchTerm ? 'No images found' : 'No images available'}
            </Typography>
            <Typography variant="body2" sx={{
                color: "text.secondary"
            }}>
                {searchTerm ? (
                    'Try adjusting your search criteria.'
                ) : (
                    <>
                        Run some apps, treat yourself, {' '}
                        <Link
                            href="https://selfh.st/apps/"
                            target="_blank"
                            rel="noopener noreferrer"
                        >
                            https://selfh.st/apps/
                        </Link>
                    </>
                )}
            </Typography>
        </Paper>
    );
};

export default ImagesPage;