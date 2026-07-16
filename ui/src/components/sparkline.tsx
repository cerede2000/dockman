import {Box} from "@mui/material";

interface SparklineProps {
    data: number[];
    color: string;
    /** rendered height in px; the drawing is stretched to the parent width */
    height?: number;
}

// Fixed drawing space, stretched to the rendered box (Dockhand's inspect
// modal geometry: viewBox 120x32 with preserveAspectRatio="none"). The
// vertical resolution is what makes small variations visible: a ±10% swing
// spans ~6px instead of drowning in a short fixed-height chart.
const VIEW_W = 120;
const VIEW_H = 32;

/**
 * Inline SVG area chart for live metric history (CPU %, memory %...),
 * reproducing Dockhand's chart math: zero-based scale with the ceiling at
 * the window maximum (floored at 1), line + translucent area fill.
 */
export function Sparkline({data, color, height = 26}: SparklineProps) {
    // no data yet: keep the footprint with a subtle placeholder
    if (data.length === 0) {
        return (
            <Box sx={{
                width: '100%',
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
    const step = VIEW_W / (series.length - 1);
    const points = series.map((v, i) => {
        const x = i * step;
        const y = VIEW_H - (Math.max(v, 0) / max) * VIEW_H;
        return `${x.toFixed(2)},${y.toFixed(2)}`;
    });

    const line = points.join(' ');
    const area = `0,${VIEW_H} ${line} ${VIEW_W},${VIEW_H}`;

    return (
        <svg
            width="100%"
            height={height}
            viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
            preserveAspectRatio="none"
            style={{display: 'block'}}
            aria-hidden
        >
            <polygon points={area} fill={color} opacity={0.2}/>
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
