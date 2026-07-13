import { useMutation, useQueryClient } from "@tanstack/react-query";

// The shared post-update choreography: run the screen-supplied PATCH, then
// refresh both the list and the specific record so the 360 reflects the new
// version. A 409 version_skew surfaces as mutation.error (rendered by the form),
// never a silent overwrite.
export function useUpdateRecord<Updated extends { id: string }>({
  update,
  invalidate,
  recordKey,
  onDone,
}: Readonly<{
  update: (values: Record<string, unknown>) => Promise<Updated>;
  invalidate: string;
  recordKey: string;
  onDone: () => void;
}>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: update,
    onSuccess: (updated) => {
      queryClient.invalidateQueries({ queryKey: [invalidate] });
      queryClient.invalidateQueries({ queryKey: [recordKey, updated.id] });
      onDone();
    },
  });
}
