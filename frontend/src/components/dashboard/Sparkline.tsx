/**
 * Sparkline — tiny area chart with no axes, no grid, no tooltip.
 *
 * Used inside stat cards under the big number. recharts is overkill for
 * a literal sparkline but it's already in the bundle for the bigger
 * charts in Sprint 2+, and ResponsiveContainer handles the parent-width
 * sizing that pure SVG would force us to compute manually.
 *
 * Each instance gets a unique gradient id via useId so multiple
 * sparklines on the same page don't share a gradient definition.
 */
import { useId } from 'react'
import { Area, AreaChart, ResponsiveContainer } from 'recharts'

export interface SparklineProps {
  data: number[]
  /** CSS colour string — typically a CSS variable like `oklch(...)`. */
  color: string
  height?: number
}

export function Sparkline({ data, color, height = 36 }: SparklineProps) {
  const reactId = useId()
  const gradientId = `spark-${reactId.replace(/:/g, '')}`
  const chartData = data.map((v, i) => ({ i, v }))

  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={chartData} margin={{ top: 2, right: 0, bottom: 0, left: 0 }}>
        <defs>
          <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%"   stopColor={color} stopOpacity={0.45} />
            <stop offset="100%" stopColor={color} stopOpacity={0.02} />
          </linearGradient>
        </defs>
        <Area
          type="monotone"
          dataKey="v"
          stroke={color}
          strokeWidth={1.75}
          fill={`url(#${gradientId})`}
          isAnimationActive={false}
        />
      </AreaChart>
    </ResponsiveContainer>
  )
}
