package gitsync

// typedConfirmationText is the single explicit opt-in used by every Git
// operation which requires keyboard confirmation. Keeping one short value
// avoids action-specific phrases drifting between the API and its clients.
const typedConfirmationText = "CONFIRM"
