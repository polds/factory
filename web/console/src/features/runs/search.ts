import type { FailureClass, RunListQuery } from "../../api/types";
import { isFailureClass } from "./format";
import { FILTER_KEYS, type FilterKey, type RunFilters } from "./types";

export const EMPTY_FILTERS: RunFilters = {
  agent: "",
  status: "",
  source: "",
  model: "",
  worker: "",
  failure_class: "",
};

export function filtersFromSearch(search: string): RunFilters {
  const params = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const filters = { ...EMPTY_FILTERS };
  for (const key of FILTER_KEYS) {
    filters[key] = params.get(key) ?? "";
  }
  return filters;
}

export function filtersToPath(filters: RunFilters): string {
  const params = new URLSearchParams();
  for (const key of FILTER_KEYS) {
    const value = filters[key];
    if (value) params.set(key, value);
  }
  const qs = params.toString();
  return qs ? `/runs?${qs}` : "/runs";
}

export function withFilter(filters: RunFilters, key: FilterKey, value: string): RunFilters {
  return { ...filters, [key]: value };
}

export function activeFilterCount(filters: RunFilters): number {
  let count = 0;
  for (const key of FILTER_KEYS) {
    if (filters[key]) count += 1;
  }
  return count;
}

export function filtersToQuery(filters: RunFilters): RunListQuery {
  const failureClass: FailureClass | undefined = isFailureClass(filters.failure_class)
    ? filters.failure_class
    : undefined;
  return {
    failure_class: failureClass,
    source: filters.source || undefined,
    agent: filters.agent || undefined,
    status: filters.status || undefined,
    worker: filters.worker || undefined,
    model: filters.model || undefined,
  };
}
