export const YAML_OUTLINE_SECTIONS = ['services', 'networks', 'volumes', 'secrets'] as const;

export type YamlOutlineSection = typeof YAML_OUTLINE_SECTIONS[number];

export interface YamlOutlineItem {
    section: YamlOutlineSection;
    name: string;
    line: number;
    column: number;
    level: 0 | 1;
}

interface YamlKey {
    name: string;
    indent: number;
    column: number;
}

// This intentionally reads only mapping keys and indentation. It remains useful
// while a Compose file is temporarily invalid and does not require a second YAML
// parser beside the validator used by the backend.
const readMappingKey = (line: string): YamlKey | null => {
    const match = /^( *)(?:"((?:\\.|[^"\\])*)"|'((?:''|[^'])*)'|([^:#][^:]*?))\s*:(?:\s|$)/.exec(line);
    if (!match) return null;

    const rawName = match[1 + (match[2] !== undefined ? 1 : match[3] !== undefined ? 2 : 3)] ?? '';
    const name = match[2] !== undefined
        ? rawName.replace(/\\"/g, '"')
        : match[3] !== undefined
            ? rawName.replace(/''/g, "'")
            : rawName.trim();
    if (!name || name === '<<') return null;

    return {name, indent: match[1].length, column: match[1].length + 1};
};

export const buildYamlOutline = (contents: string): YamlOutlineItem[] => {
    const result: YamlOutlineItem[] = [];
    let currentSection: YamlOutlineSection | null = null;
    let childIndent: number | null = null;

    contents.split(/\r?\n/).forEach((line, index) => {
        const key = readMappingKey(line);
        if (!key) return;

        if (key.indent === 0) {
            currentSection = YAML_OUTLINE_SECTIONS.includes(key.name as YamlOutlineSection)
                ? key.name as YamlOutlineSection
                : null;
            childIndent = null;
            if (currentSection) {
                result.push({
                    section: currentSection,
                    name: currentSection,
                    line: index + 1,
                    column: key.column,
                    level: 0,
                });
            }
            return;
        }

        if (!currentSection) return;
        if (childIndent === null) childIndent = key.indent;
        if (key.indent !== childIndent) return;

        result.push({
            section: currentSection,
            name: key.name,
            line: index + 1,
            column: key.column,
            level: 1,
        });
    });

    return result;
};
