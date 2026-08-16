// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { Modal, Skeleton } from "../design-system/atoms";
import { PipelineLadder } from "../design-system/pipelineladder";
import { SurfaceState } from "../design-system/surfacestate";
import { useT } from "../i18n";
import { useProviderLabel } from "./channelproviders";
import { throwProblem } from "./common";

// The drill-down: what the pipeline did with ONE message, step by step.
//
// A right-anchored drawer rather than a centred dialog, because the reader is
// checking this rung against the row they clicked, and the list behind it is
// the thing they are comparing against.
//
// The ladder itself holds no knowledge of the pipeline — it walks whatever the
// server sends. So a stage added later appears here with no change to this file
// and no frontend release, which is the whole reason the surface is shaped this
// way rather than as a fixed set of fields.

export function CaptureActivityDrawer({
  traceId,
  onClose,
}: Readonly<{ traceId: string; onClose: () => void }>) {
  const t = useT();
  const providerLabel = useProviderLabel();
  const trace = useQuery({
    queryKey: ["capture-trace-pipeline", traceId],
    queryFn: async () => {
      const { data, error } = await api.GET("/capture/traces/{id}", {
        params: { path: { id: traceId } },
      });
      if (error) throwProblem(error);
      return data;
    },
  });

  return (
    <Modal
      open
      onClose={onClose}
      labelledBy="capture-pipeline-title"
      placement="right"
    >
      <h2 id="capture-pipeline-title">{t("pipeline.title")}</h2>
      <p className="capture-activity__drawer-sub">{t("pipeline.sub")}</p>
      {trace.isPending ? (
        <Skeleton width="100%" height={240} />
      ) : trace.data ? (
        <>
          {trace.data.connector && (
            <p className="capture-activity__drawer-transport">
              {t("pipeline.transport")}{" "}
              <strong>{providerLabel(trace.data.connector)}</strong>
            </p>
          )}
          <PipelineLadder
            stages={trace.data.stages}
            payloadsEnabled={trace.data.payload_capture_enabled}
          />
        </>
      ) : (
        // `unavailable` rather than `empty`: the ladder always has a rung per
        // registered stage, so nothing here means the read failed — and drawing
        // that as "there are none" would state a fact about the pipeline that
        // we do not have. emptyLabel is required by the component and unused by
        // this state; it names what there would be none OF, if the state were
        // ever `empty`.
        <SurfaceState
          state="unavailable"
          emptyLabel={t("pipeline.unavailable")}
        >
          {null}
        </SurfaceState>
      )}
    </Modal>
  );
}
