import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { throwProblem } from "../screens/common";

// The installation's own settings, and the one field on them a form has to act
// on: how large a file this deployment accepts.
//
// The number is the OPERATOR's, not the build's — an installation sets its own
// ceiling in its deployment file, so a constant compiled in here would be right
// only for whoever shipped the default. It arrives on the installation read
// every role may make.
//
// Why a client wants it at all, when the server refuses an oversize upload
// anyway: because refusing afterwards means the whole file crossed the wire
// first. Told the limit, a form can state it before a file is chosen and refuse
// in the instant one is.
//
// ONE query for both readers — the settings screen and any upload form — under
// one key. Two hooks on the same key would be worse than two keys: whichever
// mounted first would decide the behaviour of the other, and only sometimes.

/** The shared query key. Exported so a mutation can invalidate this read. */
export const INSTALLATION_SETTINGS_KEY = ["installation-settings"] as const;

/**
 * The installation's settings, as the whole query.
 *
 * The queryFn throws, so a screen showing this record can gate on `error` and
 * say what went wrong. A caller that only wants a field reads `data?.field` and
 * gets undefined either way — no second request, and no second opinion about
 * what a failure means.
 */
export function useInstallationSettings() {
  return useQuery({
    queryKey: INSTALLATION_SETTINGS_KEY,
    queryFn: async () => {
      const { data, error, response } = await api.GET("/installation/settings");
      if (error || !response.ok) {
        throwProblem(error);
      }
      return data;
    },
  });
}

/**
 * The largest upload this installation accepts, in bytes — or undefined while
 * the answer is still on its way, or if it never comes.
 *
 * Undefined is deliberately NOT a number. Guessing a limit before the server
 * has stated one produces this feature's own failure in reverse: a form that
 * refuses a file the installation would have taken, over a number the client
 * invented and the reader cannot argue with. With no answer the upload proceeds
 * and the refusal stays where it was before any of this existed — on the
 * server, which is the authority regardless.
 */
export function useMaxUploadBytes(): number | undefined {
  return useInstallationSettings().data?.max_upload_bytes;
}

/**
 * The ceiling as a person reading a form would write it: decimal MB, exactly.
 *
 * Decimal because that is what the server enforces and what its refusal says. A
 * binary megabyte here would state a limit 4.8% larger than the real one, and
 * the reader who believed it would then be refused by a server that had already
 * told them twice, in two different numbers.
 */
export function formatUploadLimit(bytes: number): string {
  const mb = bytes / 1_000_000;
  return `${Number.isInteger(mb) ? mb : mb.toFixed(1)} MB`;
}
