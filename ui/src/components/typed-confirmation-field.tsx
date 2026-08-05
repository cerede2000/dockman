import TextField from '@mui/material/TextField';
import type {FocusEventHandler} from 'react';

export const TYPED_CONFIRMATION = 'CONFIRM';

export default function TypedConfirmationField({
    value,
    onChange,
    onBlur,
    autoFocus = false,
}: {
    value: string;
    onChange: (value: string) => void;
    onBlur?: FocusEventHandler<HTMLInputElement | HTMLTextAreaElement>;
    autoFocus?: boolean;
}) {
    return <TextField
        autoFocus={autoFocus}
        fullWidth
        size="small"
        label={`Type "${TYPED_CONFIRMATION}" to confirm`}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onBlur={onBlur}
        slotProps={{htmlInput: {autoComplete: 'off', spellCheck: false}}}
    />;
}
