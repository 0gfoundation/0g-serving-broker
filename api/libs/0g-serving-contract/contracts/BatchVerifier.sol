// SPDX-License-Identifier: Unlicense
pragma solidity >=0.7.0 <0.9.0;

library BatchVerifier {
    function GroupOrder() public pure returns (uint256) {
        return 21888242871839275222246405745257275088548364400416034343698204186575808495617;
    }

    function NegateY(uint256 Y) internal pure returns (uint256) {
        uint256 q = 21888242871839275222246405745257275088696311157297823662689037894645226208583;
        return q - (Y % q);
    }

    function accumulate(
        uint256[] memory in_proof,
        uint256[] memory proof_inputs, // public inputs, length is num_inputs * num_proofs
        uint256 num_proofs
    ) internal view returns (uint256[] memory proofsAandC, uint256[] memory inputAccumulators) {
        uint256 q = 21888242871839275222246405745257275088548364400416034343698204186575808495617;
        uint256 numPublicInputs = proof_inputs.length / num_proofs;
        uint256[] memory entropy = new uint256[](num_proofs);
        inputAccumulators = new uint256[](numPublicInputs + 1);

        for (uint256 proofNumber = 0; proofNumber < num_proofs; proofNumber++) {
            if (proofNumber == 0) {
                entropy[proofNumber] = 1;
            } else {
                entropy[proofNumber] = uint256(blockhash(block.number - proofNumber)) % q;
            }
            require(entropy[proofNumber] != 0, "Entropy should not be zero");
            // here multiplication by 1 is implied
            inputAccumulators[0] = addmod(inputAccumulators[0], entropy[proofNumber], q);
            for (uint256 i = 0; i < numPublicInputs; i++) {
                // accumulate the exponent with extra entropy mod q
                inputAccumulators[i + 1] = addmod(
                    inputAccumulators[i + 1],
                    mulmod(entropy[proofNumber], proof_inputs[proofNumber * numPublicInputs + i], q),
                    q
                );
            }
            // coefficient for +vk.alpha (mind +) is the same as inputAccumulator[0]
        }

        // inputs for scalar multiplication
        uint256[3] memory mul_input;
        bool success;

        // use scalar multiplications to get proof.A[i] * entropy[i]

        proofsAandC = new uint256[](num_proofs * 2 + 2);

        proofsAandC[0] = in_proof[0];
        proofsAandC[1] = in_proof[1];

        for (uint256 proofNumber = 1; proofNumber < num_proofs; proofNumber++) {
            mul_input[0] = in_proof[proofNumber * 8];
            mul_input[1] = in_proof[proofNumber * 8 + 1];
            mul_input[2] = entropy[proofNumber];
            assembly {
                // ECMUL, output proofsA[i]
                success := staticcall(sub(gas(), 2000), 7, mul_input, 0x60, mul_input, 0x40)
            }
            proofsAandC[proofNumber * 2] = mul_input[0];
            proofsAandC[proofNumber * 2 + 1] = mul_input[1];
            require(success, "Failed to call a precompile");
        }

        // use scalar multiplication and addition to get sum(proof.C[i] * entropy[i])

        uint256[4] memory add_input;

        add_input[0] = in_proof[6];
        add_input[1] = in_proof[7];

        for (uint256 proofNumber = 1; proofNumber < num_proofs; proofNumber++) {
            mul_input[0] = in_proof[proofNumber * 8 + 6];
            mul_input[1] = in_proof[proofNumber * 8 + 7];
            mul_input[2] = entropy[proofNumber];
            assembly {
                // ECMUL, output proofsA
                success := staticcall(sub(gas(), 2000), 7, mul_input, 0x60, add(add_input, 0x40), 0x40)
            }
            require(success, "Failed to call a precompile for G1 multiplication for Proof C");

            assembly {
                // ECADD from two elements that are in add_input and output into first two elements of add_input
                success := staticcall(sub(gas(), 2000), 6, add_input, 0x80, add_input, 0x40)
            }
            require(success, "Failed to call a precompile for G1 addition for Proof C");
        }

        proofsAandC[num_proofs * 2] = add_input[0];
        proofsAandC[num_proofs * 2 + 1] = add_input[1];
    }

    function prepareBatches(
        uint256[14] memory in_vk,
        uint256[] memory vk_gammaABC,
        uint256[] memory inputAccumulators
    ) internal view returns (uint256[4] memory finalVksAlphaX) {
        // Compute the linear combination vk_x using accumulator
        // First two fields are used as the sum and are initially zero
        uint256[4] memory add_input;
        uint256[3] memory mul_input;
        bool success;

        // Performs a sum(gammaABC[i] * inputAccumulator[i])
        for (uint256 i = 0; i < inputAccumulators.length; i++) {
            mul_input[0] = vk_gammaABC[2 * i];
            mul_input[1] = vk_gammaABC[2 * i + 1];
            mul_input[2] = inputAccumulators[i];

            assembly {
                // ECMUL, output to the last 2 elements of `add_input`
                success := staticcall(sub(gas(), 2000), 7, mul_input, 0x60, add(add_input, 0x40), 0x40)
            }
            require(success, "Failed to call a precompile for G1 multiplication for input accumulator");

            assembly {
                // ECADD from four elements that are in add_input and output into first two elements of add_input
                success := staticcall(sub(gas(), 2000), 6, add_input, 0x80, add_input, 0x40)
            }
            require(success, "Failed to call a precompile for G1 addition for input accumulator");
        }

        finalVksAlphaX[2] = add_input[0];
        finalVksAlphaX[3] = add_input[1];

        // add one extra memory slot for scalar for multiplication usage
        uint256[3] memory finalVKalpha;
        finalVKalpha[0] = in_vk[0];
        finalVKalpha[1] = in_vk[1];
        finalVKalpha[2] = inputAccumulators[0];

        assembly {
            // ECMUL, output to first 2 elements of finalVKalpha
            success := staticcall(sub(gas(), 2000), 7, finalVKalpha, 0x60, finalVKalpha, 0x40)
        }
        require(success, "Failed to call a precompile for G1 multiplication");
        finalVksAlphaX[0] = finalVKalpha[0];
        finalVksAlphaX[1] = finalVKalpha[1];
    }

    // original equation
    // e(proof.A, proof.B)*e(-vk.alpha, vk.beta)*e(-vk_x, vk.gamma)*e(-proof.C, vk.delta) == 1
    // accumulation of inputs
    // gammaABC[0] + sum[ gammaABC[i+1]^proof_inputs[i] ]

    function BatchVerify(
        uint256[14] memory in_vk,
        uint256[] memory vk_gammaABC,
        uint256[] memory in_proof,
        uint256[] memory proof_inputs,
        uint256 num_proofs
    ) internal view returns (bool) {
        require(in_proof.length == num_proofs * 8, "Invalid proofs length for a batch");
        require(proof_inputs.length % num_proofs == 0, "Invalid inputs length for a batch");
        require(((vk_gammaABC.length / 2) - 1) == proof_inputs.length / num_proofs);

        // strategy is to accumulate entropy separately for some proof elements
        // (accumulate only for G1, can't in G2) of the pairing equation, as well as input verification key,
        // postpone scalar multiplication as much as possible and check only one equation
        // by using 3 + num_proofs pairings only plus 2*num_proofs + (num_inputs+1) + 1 scalar multiplications compared to naive
        // 4*num_proofs pairings and num_proofs*(num_inputs+1) scalar multiplications

        uint256[] memory proofsAandC;
        uint256[] memory inputAccumulators;
        (proofsAandC, inputAccumulators) = accumulate(in_proof, proof_inputs, num_proofs);

        uint256[4] memory finalVksAlphaX = prepareBatches(in_vk, vk_gammaABC, inputAccumulators);

        uint256[] memory inputs = new uint256[](6 * num_proofs + 18);
        // first num_proofs pairings e(ProofA, ProofB)
        for (uint256 proofNumber = 0; proofNumber < num_proofs; proofNumber++) {
            inputs[proofNumber * 6] = proofsAandC[proofNumber * 2];
            inputs[proofNumber * 6 + 1] = proofsAandC[proofNumber * 2 + 1];
            inputs[proofNumber * 6 + 2] = in_proof[proofNumber * 8 + 2];
            inputs[proofNumber * 6 + 3] = in_proof[proofNumber * 8 + 3];
            inputs[proofNumber * 6 + 4] = in_proof[proofNumber * 8 + 4];
            inputs[proofNumber * 6 + 5] = in_proof[proofNumber * 8 + 5];
        }

        // second pairing e(-finalVKaplha, vk.beta)
        inputs[num_proofs * 6] = finalVksAlphaX[0];
        inputs[num_proofs * 6 + 1] = NegateY(finalVksAlphaX[1]);
        inputs[num_proofs * 6 + 2] = in_vk[2];
        inputs[num_proofs * 6 + 3] = in_vk[3];
        inputs[num_proofs * 6 + 4] = in_vk[4];
        inputs[num_proofs * 6 + 5] = in_vk[5];

        // third pairing e(-finalVKx, vk.gamma)
        inputs[num_proofs * 6 + 6] = finalVksAlphaX[2];
        inputs[num_proofs * 6 + 7] = NegateY(finalVksAlphaX[3]);
        inputs[num_proofs * 6 + 8] = in_vk[6];
        inputs[num_proofs * 6 + 9] = in_vk[7];
        inputs[num_proofs * 6 + 10] = in_vk[8];
        inputs[num_proofs * 6 + 11] = in_vk[9];

        // fourth pairing e(-proof.C, finalVKdelta)
        inputs[num_proofs * 6 + 12] = proofsAandC[num_proofs * 2];
        inputs[num_proofs * 6 + 13] = NegateY(proofsAandC[num_proofs * 2 + 1]);
        inputs[num_proofs * 6 + 14] = in_vk[10];
        inputs[num_proofs * 6 + 15] = in_vk[11];
        inputs[num_proofs * 6 + 16] = in_vk[12];
        inputs[num_proofs * 6 + 17] = in_vk[13];

        uint256 inputsLength = inputs.length * 32;
        uint256[1] memory out;
        require(inputsLength % 192 == 0, "Inputs length should be multiple of 192 bytes");

        bool success;
        assembly {
            success := staticcall(sub(gas(), 2000), 8, add(inputs, 0x20), inputsLength, out, 0x20)
        }
        return out[0] == 1;
    }
}

contract Wrapper {
    // Verification Key data
    uint256 constant alphax = 20491192805390485299153009773594534940189261866228447918068658471970481763042;
    uint256 constant alphay = 9383485363053290200918347156157836566562967994039712273449902621266178545958;
    uint256 constant betax1 = 4252822878758300859123897981450591353533073413197771768651442665752259397132;
    uint256 constant betax2 = 6375614351688725206403948262868962793625744043794305715222011528459656738731;
    uint256 constant betay1 = 21847035105528745403288232691147584728191162732299865338377159692350059136679;
    uint256 constant betay2 = 10505242626370262277552901082094356697409835680220590971873171140371331206856;
    uint256 constant gammax1 = 11559732032986387107991004021392285783925812861821192530917403151452391805634;
    uint256 constant gammax2 = 10857046999023057135944570762232829481370756359578518086990519993285655852781;
    uint256 constant gammay1 = 4082367875863433681332203403145435568316851327593401208105741076214120093531;
    uint256 constant gammay2 = 8495653923123431417604973247489272438418190587263600148770280649306958101930;
    uint256 constant deltax1 = 579319270117363181520443676899870907237582523823698315244920836200489980253;
    uint256 constant deltax2 = 8578435627935072325442365562360251185109546730365913338147554846662627322855;
    uint256 constant deltay1 = 16667904523081213371389818234658029461862981658535247919947249526023179843151;
    uint256 constant deltay2 = 11910793343215559156827457492035811737776961152714498436708533659614763075914;

    uint256 constant IC0x = 14201891832029926424160131441791309501759422916322232758827879798816281461925;
    uint256 constant IC0y = 578966653513912274877103534238873369162913930957181085011155647838530412063;

    uint256 constant IC1x = 787171937292551559207680019429954228737171782848619510789067671990116143030;
    uint256 constant IC1y = 1068009031252894439369382560560256918960977854593428243592424841662063012567;

    uint256 constant IC2x = 1003761025231378022315374559390773936132894130459534580414540654167077765785;
    uint256 constant IC2y = 14716002031258433172318776677771923493869295741119167462693299982106355135331;

    uint256 constant IC3x = 19456634363183512362713771987896536254752271280738702863074865894790866938659;
    uint256 constant IC3y = 12903782737770462926580275565588792551840640907810773185815736317784375737679;

    uint256 constant IC4x = 17774336710712495622793073584353276932928330612484993804641705991825713463745;
    uint256 constant IC4y = 12834252138132169874062167872519663704773382645259390358457435730976075928160;

    uint256 constant IC5x = 12036627545073751024369388975495384251839834278750348332065333834161210615837;
    uint256 constant IC5y = 16435430383182548383069591683406377363147688611997540792293796019494851191013;

    uint256 constant IC6x = 7181237044294455874322563130578524594445579252694314912601048999772013046456;
    uint256 constant IC6y = 912928788050742388003599687637001703836435635835115105144389995776558708358;

    uint256 constant IC7x = 4250490053680631392970278374692976194805364161743595771563029131284024217383;
    uint256 constant IC7y = 10863904049173901193087418201788763810180614113103481756835263192797399665499;

    function getInVk() internal pure returns (uint256[14] memory) {
        return [
            alphax,
            alphay,
            betax1,
            betax2,
            betay1,
            betay2,
            gammax1,
            gammax2,
            gammay1,
            gammay2,
            deltax1,
            deltax2,
            deltay1,
            deltay2
        ];
    }

    function getVkGammaABC() internal pure returns (uint256[] memory) {
        uint256[] memory result = new uint256[](16);

        result[0] = IC0x;
        result[1] = IC0y;

        result[2] = IC1x;
        result[3] = IC1y;

        result[4] = IC2x;
        result[5] = IC2y;

        result[6] = IC3x;
        result[7] = IC3y;

        result[8] = IC4x;
        result[9] = IC4y;

        result[10] = IC5x;
        result[11] = IC5y;

        result[12] = IC6x;
        result[13] = IC6y;

        result[14] = IC7x;
        result[15] = IC7y;

        return result;
    }

    function verifyBatch(
        uint256[] calldata in_proof,
        uint256[] calldata proof_inputs,
        uint256 num_proofs
    ) public view returns (bool success) {
        return BatchVerifier.BatchVerify(getInVk(), getVkGammaABC(), in_proof, proof_inputs, num_proofs);
    }
}
