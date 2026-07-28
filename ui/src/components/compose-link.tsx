import {Typography} from "@mui/material";
import {useNavigate} from "react-router";

const ComposeLink = ({servicePath, stackName}: { servicePath: string; stackName: string; }) => {
    const navigate = useNavigate()

    return (
        <Typography
            variant="body2"
            component="span"
            sx={{
                textDecoration: 'none',
                color: 'primary.main',
                wordBreak: 'break-all',
                cursor: 'pointer',
                '&:hover': {textDecoration: 'underline'}
            }}
            onClick={(event) => {
                event.stopPropagation() // Prevent row click
                navigate(`/stacks/${servicePath}?tab=0`)
            }}
        >
            {stackName}
        </Typography>
    );
};

export default ComposeLink;
