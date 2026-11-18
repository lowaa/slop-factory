import "./column_chart.scss";

export interface ChartColumn {
  key: string;
  // Under the column, and in its hover text.
  label: string;
  // One bar per series, in the chart's series order; null is no bar (no
  // figure), which is not a zero-height one.
  values: (number | null)[];
  // What a screen reader and the hover say about the column.
  description: string;
}

interface ColumnChartProps {
  // What the chart shows, for a screen reader: the columns are drawn, not
  // read out one by one.
  ariaLabel: string;
  series: string[];
  columns: ChartColumn[];
  // Show every nth column's label, so a 90-day chart's labels do not collide.
  labelEvery?: number;
}

// ColumnChart is plain CSS columns rather than a chart library: a few bars
// per period are all the Reports tab draws. The bars are scaled to the
// tallest value shown, so a quiet period still reads at full height.
export default function ColumnChart({ ariaLabel, series, columns, labelEvery = 1 }: ColumnChartProps) {
  const max = Math.max(0, ...columns.flatMap((c) => c.values.filter((v): v is number => v !== null)));
  return (
    <div className="column-chart" role="img" aria-label={ariaLabel}>
      <div className="column-chart__plot">
        {columns.map((column) => (
          <div key={column.key} className="column-chart__column" title={column.description}>
            <div className="column-chart__bars">
              {column.values.map((value, i) => (
                <div
                  key={series[i]}
                  className={`column-chart__bar column-chart__bar--series-${i}`}
                  style={{ height: value === null || max === 0 ? 0 : `${Math.max(2, (value / max) * 100)}%` }}
                />
              ))}
            </div>
          </div>
        ))}
      </div>
      <div className="column-chart__labels">
        {columns.map((column, i) => (
          <span key={column.key} className="column-chart__label">
            {i % labelEvery === 0 ? column.label : ""}
          </span>
        ))}
      </div>
      {series.length > 1 && (
        <ul className="column-chart__legend">
          {series.map((name, i) => (
            <li key={name}>
              <span className={`column-chart__swatch column-chart__bar--series-${i}`} /> {name}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
