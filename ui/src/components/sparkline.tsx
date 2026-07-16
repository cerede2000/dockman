import {Box} from "@mui/material";

interface SparklineProps {
    data: number[];
    color: string;
    width?: number;
    height?: number;
    /** fixed scale ceiling; defaults to the data max (auto-scale) */
    max?: number;
}

/**
 * Tiny inline SVG area chart for live metric history (CPU, memory...).
 * Pure geometry — no chart library, cheap enough to render per table row
 * on every poll tick.
 */
export function Sparkline({data, color, width = 96, height = 22, max}: SparklineProps) {
    // keep the cell footprint stable while history builds up
    if (data.length < 2) {
        return <Box sx={{width, height}}/>;
    }

    const ceiling = Math.max(max ?? Math.max(...data), 0.001);
    const stepX = width / (data.length - 1);
    const points = data.map((v, i) => {
        const clamped = Math.min(Math.max(v, 0), ceiling);
        const x = i * stepX;
        const y = height - 1 - (clamped / ceiling) * (height - 2);
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    });

    const line = points.join(' ');
    const area = `0,${height} ${line} ${width},${height}`;

    return (
        <svg width={width} height={height} style={{display: 'block'}} aria-hidden>
            <polygon points={area} fill={color} opacity={0.16}/>
            <polyline
                points={line}
                fill="none"
                stroke={color}
                strokeWidth={1.5}
                strokeLinejoin="round"
                strokeLinecap="round"
            />
        </svg>
    );
}

export default Sparkline;
