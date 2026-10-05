import type { CSSProperties } from "react";
import { number } from "./model";

export function sliderPosition(
  value: number,
  max: number,
  logarithmic = false,
) {
  const bounded = Math.min(
    max,
    Math.max(0, Number.isFinite(value) ? value : 0),
  );
  if (max <= 0) return 0;
  return logarithmic ? Math.log1p(bounded) / Math.log1p(max) : bounded / max;
}

export function sliderValue(
  position: number,
  max: number,
  logarithmic = false,
) {
  const ratio = Math.min(1, Math.max(0, position));
  if (ratio === 0 || max <= 0) return 0;
  if (ratio === 1) return max;
  return logarithmic
    ? Number(Math.expm1(ratio * Math.log1p(max)).toPrecision(4))
    : Math.round(ratio * max);
}

export function LoadSlider({
  label,
  value,
  max,
  unit,
  logarithmic = false,
  disabled = false,
  onChange,
}: {
  label: string;
  value: number;
  max: number;
  unit: string;
  logarithmic?: boolean;
  disabled?: boolean;
  onChange: (value: number) => void;
}) {
  const position = sliderPosition(value, max, logarithmic);
  const ticks = logarithmic
    ? [0, 10, 1000, 100_000, max].filter(
        (v, i, all) => v <= max && all.indexOf(v) === i,
      )
    : [
        0,
        Math.round(max * 0.25),
        Math.round(max * 0.5),
        Math.round(max * 0.75),
        max,
      ].filter((v, i, all) => all.indexOf(v) === i);
  const style = { "--slider-fill": `${position * 100}%` } as CSSProperties;
  const pretty = (n: number) => (n >= 1000 ? number(n) : String(n));
  const tickLabel = (n: number) =>
    new Intl.NumberFormat("en-US", {
      notation: "compact",
      maximumFractionDigits: 1,
    }).format(n);
  return (
    <div
      className={`load-slider ${disabled ? "is-disabled" : ""}`}
      style={style}
    >
      <div className="load-slider-readout" aria-hidden="true">
        <span>{logarithmic ? "LOGARITHMIC SCALE" : "LINEAR SCALE"}</span>
        <strong>
          {pretty(value)} <small>{unit}</small>
        </strong>
      </div>
      <div className="load-slider-track">
        <input
          aria-label={label}
          name={label}
          autoComplete="off"
          aria-valuemin={0}
          aria-valuemax={max}
          aria-valuenow={
            Number.isFinite(value) ? Math.min(max, Math.max(0, value)) : 0
          }
          aria-valuetext={`${value} ${unit}${logarithmic ? ", logarithmic scale" : ""}`}
          type="range"
          min={0}
          max={logarithmic ? 1000 : max}
          step={1}
          value={
            logarithmic
              ? Math.round(position * 1000)
              : Math.round(position * max)
          }
          disabled={disabled}
          onChange={(event) =>
            onChange(
              sliderValue(
                Number(event.target.value) / (logarithmic ? 1000 : max || 1),
                max,
                logarithmic,
              ),
            )
          }
        />
      </div>
      <div className="load-slider-ticks" aria-hidden="true">
        {ticks.map((tick) => (
          <span
            key={tick}
            style={{ left: `${sliderPosition(tick, max, logarithmic) * 100}%` }}
          >
            {tickLabel(tick)}
          </span>
        ))}
      </div>
    </div>
  );
}
