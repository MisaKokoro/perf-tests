// Keep the hot path to one shard lookup, integer geometry and one URL lookup.
export function regionRequest(spec, objects, shard, iteration) {
  const g = shard[iteration % spec.region_count];
  const object = Math.floor(g / spec.regions_per_object);
  const start = (g % spec.regions_per_object) * spec.range_bytes;
  const end = start + spec.range_bytes - 1;
  const url = objects[object];
  if (!Number.isSafeInteger(iteration) || iteration < 0 ||
      !Number.isSafeInteger(g) || g < 0 || !Number.isSafeInteger(end) ||
      typeof url !== 'string' || end < start) {
    throw new Error('invalid fixed-pool plan mapping');
  }
  return { url, range: `bytes=${start}-${end}` };
}
