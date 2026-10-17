import type { HarnessUsage } from "../../api/types";
import { Caveat } from "./caveat";
import { formatCost, formatCount } from "./format";

export function UsageTab({ usage }: { usage: HarnessUsage }) {
  return (
    <div className="usage">
      <div className="usage-totals">
        <div>
          <p className="muted">Input tokens</p>
          <p className="mono usage-total" data-testid="usage-input-tokens">
            {formatCount(usage.total.input_tokens)}
          </p>
        </div>
        <div>
          <p className="muted">Output tokens</p>
          <p className="mono usage-total" data-testid="usage-output-tokens">
            {formatCount(usage.total.output_tokens)}
          </p>
        </div>
        <div>
          <p className="muted">Cost</p>
          <p className="mono usage-total" data-testid="usage-cost">
            {formatCost(usage.total.cost_cents)}
          </p>
        </div>
      </div>
      <p className="section-label">By model</p>
      <table className="data-table">
        <thead>
          <tr>
            <th scope="col">Model</th>
            <th scope="col">In</th>
            <th scope="col">Out</th>
            <th scope="col">Cost</th>
          </tr>
        </thead>
        <tbody>
          {usage.by_model.map((row) => (
            <tr key={row.model_id}>
              <td className="mono">{row.model_id}</td>
              <td className="mono">{formatCount(row.input_tokens)}</td>
              <td className="mono">{formatCount(row.output_tokens)}</td>
              <td className="mono">{formatCost(row.cost_cents)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="section-label">Per call</p>
      <table className="data-table">
        <thead>
          <tr>
            <th scope="col">Seq</th>
            <th scope="col">Turn</th>
            <th scope="col">Model</th>
            <th scope="col">In</th>
            <th scope="col">Out</th>
            <th scope="col">Cost</th>
          </tr>
        </thead>
        <tbody>
          {usage.calls.map((call) => (
            <tr key={`${call.seq}-${call.turn}`}>
              <td className="mono">{call.seq}</td>
              <td className="mono">{call.turn}</td>
              <td className="mono">{call.model_id}</td>
              <td className="mono">{formatCount(call.input_tokens)}</td>
              <td className="mono">{formatCount(call.output_tokens)}</td>
              <td className="mono">{formatCost(call.cost_cents)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <Caveat>Totals are the harness usage report for this run.</Caveat>
    </div>
  );
}
