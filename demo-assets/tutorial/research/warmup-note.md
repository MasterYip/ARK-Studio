# Does learning-rate warm-up remove early loss spikes?

*A two-page tutorial note. The numbers are fictional and exist to show ARK's
workflow, not to make a scientific claim.*

## Question

Small transformer models (about 60M parameters) sometimes show sharp loss
spikes in the first few thousand steps. Does a 500-step linear warm-up of the
learning rate remove them without hurting the final loss?

## Setup

- Model: 60M-parameter decoder, AdamW, peak learning rate 6e-4.
- Arms: **no warm-up** vs **500-step linear warm-up**, cosine decay after.
- 3 seeds per arm, 5,000 steps each, one GPU with a 20 GB memory claim.
- Spike: a step whose loss is more than 1.5 times its 50-step running mean.

## Result

| Arm | Final loss (mean) | Spikes per run |
|---|---|---|
| no warm-up | 2.14 | 3.0 |
| 500-step warm-up | 2.08 | 0.0 |

Warm-up removed every early spike in all three seeds and did not slow
convergence. See `results.csv` and `results-chart.svg`.

## Limits

Three seeds and one model size. The claim "warm-up keeps the final loss" stays
**open** until the learning-rate sweep (X-0002) finishes.

## References

Goyal et al. 2017 (warm-up for large-batch SGD); Liu et al. 2020 (RAdam and
the variance of adaptive learning rates). BibTeX in `references.bib`.
