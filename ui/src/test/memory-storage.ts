// memoryStorage is a Storage kept in a Map, for tests that exercise zustand's
// persist middleware. Node 25 exposes its own experimental localStorage, which
// throws without a backing file, while the CI's Node 24 serves jsdom's: a test
// relying on the global would pass on one and fail on the other.
export function memoryStorage(initial: Record<string, string> = {}): Storage {
    const data = new Map(Object.entries(initial));
    return {
        get length() {
            return data.size;
        },
        clear: () => data.clear(),
        getItem: (key: string) => data.get(key) ?? null,
        key: (index: number) => [...data.keys()][index] ?? null,
        removeItem: (key: string) => {
            data.delete(key);
        },
        setItem: (key: string, value: string) => {
            data.set(key, String(value));
        },
    };
}
