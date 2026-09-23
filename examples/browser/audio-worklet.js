class PCMPlayerProcessor extends AudioWorkletProcessor {
    constructor() {
        super();

        this.queue = [];
        this.queueOffset = 0;

        this.activeGeneration = null;

        this.playedFrames = 0;
        this.lastReportedFrames = 0;

        // Report playback progress approximately every 100 ms.
        this.reportIntervalFrames =
            Math.max(
                128,
                Math.floor(sampleRate / 10)
            );

        this.port.onmessage = (event) => {
            const message = event.data;

            switch (message.type) {
                case "generation":
                    this.setGeneration(
                        message.generationId
                    );
                    break;

                case "audio":
                    this.enqueue(
                        message.generationId,
                        message.samples
                    );
                    break;

                case "clear":
                    this.clear(
                        message.generationId
                    );
                    break;
            }
        };
    }

    setGeneration(generationId) {
        if (
            this.activeGeneration ===
            generationId
        ) {
            return;
        }

        this.queue = [];
        this.queueOffset = 0;

        this.playedFrames = 0;
        this.lastReportedFrames = 0;

        this.activeGeneration =
            generationId;
    }

    enqueue(generationId, samples) {
        if (
            !generationId ||
            generationId !==
                this.activeGeneration
        ) {
            return;
        }

        if (!samples) {
            return;
        }

        const data =
            samples instanceof Float32Array
                ? samples
                : new Float32Array(samples);

        if (data.length === 0) {
            return;
        }

        this.queue.push({
            generationId,
            samples: data
        });
    }

    clear(generationId) {
        if (
            generationId &&
            generationId !==
                this.activeGeneration
        ) {
            return;
        }

        // Send the final playback position before discarding
        // the remaining queued audio.
        this.reportProgress(true);

        this.queue = [];
        this.queueOffset = 0;

        this.activeGeneration = null;
    }

    reportProgress(force = false) {
        if (!this.activeGeneration) {
            return;
        }

        const delta =
            this.playedFrames -
            this.lastReportedFrames;

        if (
            !force &&
            delta < this.reportIntervalFrames
        ) {
            return;
        }

        this.lastReportedFrames =
            this.playedFrames;

        this.port.postMessage({
            type: "playback.progress",

            generationId:
                this.activeGeneration,

            playedFrames:
                this.playedFrames,

            sampleRate
        });
    }

    process(inputs, outputs) {
        const output = outputs[0];

        if (
            !output ||
            output.length === 0
        ) {
            return true;
        }

        const frames =
            output[0].length;

        for (
            let channel = 0;
            channel < output.length;
            channel++
        ) {
            output[channel].fill(0);
        }

        let outputOffset = 0;
        let consumedFrames = 0;

        while (
            outputOffset < frames &&
            this.queue.length > 0
        ) {
            const item =
                this.queue[0];

            if (
                item.generationId !==
                this.activeGeneration
            ) {
                this.queue.shift();
                this.queueOffset = 0;
                continue;
            }

            const current =
                item.samples;

            const available =
                current.length -
                this.queueOffset;

            const required =
                frames -
                outputOffset;

            const count =
                Math.min(
                    available,
                    required
                );

            for (
                let i = 0;
                i < count;
                i++
            ) {
                const sample =
                    current[
                        this.queueOffset + i
                    ];

                for (
                    let channel = 0;
                    channel < output.length;
                    channel++
                ) {
                    output[channel][
                        outputOffset + i
                    ] = sample;
                }
            }

            this.queueOffset += count;
            outputOffset += count;
            consumedFrames += count;

            if (
                this.queueOffset >=
                current.length
            ) {
                this.queue.shift();
                this.queueOffset = 0;
            }
        }

        if (consumedFrames > 0) {
            this.playedFrames +=
                consumedFrames;

            this.reportProgress(false);
        }

        return true;
    }
}

registerProcessor(
    "pcm-player",
    PCMPlayerProcessor
);