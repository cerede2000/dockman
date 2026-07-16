import {Box} from "@mui/material";

interface SparklineProps {
    data: number[];
    color: string;
    width?: number;
    height?: number;
}

/**
 * Tiny inline SVG area chart for live metric history (CPU %, memory %...).
 *
 * Scaling matches what most container dashboards do: zero-based, ceiling at
 * the window maximum (floored at 1 so idle noise isn't amplified). A
 * container hovering around 3% CPU zigzags across the chart height; one
 * pinned at 99% of its memory limit draws along the top.
 */
export function Sparkline({data, color, width = 96, height = 18}: SparklineProps) {
    // no data yet: keep the footprint with a subtle placeholder
    if (data.length === 0) {
        return (
            <Box sx={{
                width,
                height,
                borderRadius: 0.5,
                bgcolor: 'rgba(255,255,255,0.06)',
            }}/>
        );
    }

    // a single reading draws a flat line so the chart shows up on the very
    // first poll instead of after two ticks
    const series = data.length === 1 ? [data[0], data[0]] : data;

    const max = Math.max(...series, 1);
    const step = width / (series.length - 1);
    const points = series.map((v, i) => {
        const x = i * step;
        const y = height - (Math.max(v, 0) / max) * height;
        return `${x.toFixed(2)},${y.toFixed(2)}`;
    });

    const line = points.join(' ');
    const area = `0,${height} ${line} ${width},${height}`;

    return (
        <svg width={width} height={height} style={{display: 'block'}} aria-hidden>
            <polygon points={area} fill={color} opacity={0.15}/>
            <polyline
                points={line}
                fill="none"
                stroke={color}
                strokeWidth={1.2}
                strokeLinejoin="round"
                strokeLinecap="round"
            />
        </svg>
    );
}

export default Sparkline;
