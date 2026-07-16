import {Box} from "@mui/material";

interface SparklineProps {
    data: number[];
    color: string;
    width?: number;
    height?: number;
}

/**
 * Tiny inline SVG area chart for live metric history (CPU, memory...).
 * Pure geometry — no chart library, cheap enough to render per table row
 * on every poll tick.
 *
 * The series is min-max normalized: the window's variance spans the full
 * chart height, so a container hovering around a steady value still shows
 * its movement instead of a line pinned to the top or bottom.
 */
export function Sparkline({data, color, width = 96, height = 22}: SparklineProps) {
    // keep the cell footprint stable while history builds up
    if (data.length === 0) {
        return <Box sx={{width, height}}/>;
    }

    // a single reading still draws (a flat line) so the chart shows up on the
    // very first poll instead of after two ticks
    const series = data.length === 1 ? [data[0], data[0]] : data;

    const min = Math.min(...series);
    const max = Math.max(...series);
    const span = max - min;

    const stepX = width / (series.length - 1);
    const mid = height / 2;
    const points = series.map((v, i) => {
        const x = i * stepX;
        // constant series -> flat mid-line rather than a division by zero
        const y = span > 0 ? height - 1 - ((v - min) / span) * (height - 2) : mid;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    });

    const line = points.join(' ');
    const area = `0,${height} ${line} ${width},${height}`;

    return (
        <svg width={width} height={height} style={{display: 'block'}} aria-hidden>
            <polygon points={area} fill={color} opacity={0.14}/>
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
